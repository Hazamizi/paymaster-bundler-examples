/**
 * Compare strategies / baselines over the same 7-day window as paper-7day.
 */
import { readFileSync, existsSync, copyFileSync } from "fs";
import { dirname, join } from "path";
import { fileURLToPath } from "url";
import { applyPaperFill, defaultPaperSeed } from "./lib/paper-book.mjs";
import { runStrategy } from "./strategies/index.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const configPath = join(__dirname, "algo.config.json");
const examplePath = join(__dirname, "algo.config.example.json");
if (!existsSync(configPath) && existsSync(examplePath)) {
  copyFileSync(examplePath, configPath);
}
const baseCfg = JSON.parse(readFileSync(configPath, "utf8"));
const days = 7;
const warmupHours = 120;
const lookback = Number(baseCfg.candleLookback ?? 120);
const feePct = Number(baseCfg.feeBufferPct ?? 1.0);
const quoteSizeWant = Number(baseCfg.quoteSize ?? 250);
const reserve = Number(baseCfg.cashReserve ?? 1);
const minQuote = Number(baseCfg.minQuoteSize ?? 5);
const maxDailyLoss = Math.abs(Number(baseCfg.maxDailyLossEur ?? 15));
const maxDailyTrades = Number(baseCfg.maxDailyTrades ?? 8);
const seed = defaultPaperSeed(baseCfg, "EUR");

async function fetchHourly(product, hours) {
  const out = [];
  const end = Math.floor(Date.now() / 1000);
  const start = end - hours * 3600;
  let cursor = end;
  for (let k = 0; k < 12; k++) {
    const url = new URL(
      `https://api.coinbase.com/api/v3/brokerage/market/products/${product}/candles`,
    );
    url.searchParams.set("granularity", "ONE_HOUR");
    url.searchParams.set("start", String(Math.max(start, cursor - 300 * 3600)));
    url.searchParams.set("end", String(cursor));
    const res = await fetch(url);
    if (!res.ok) throw new Error(`${product} ${res.status}`);
    const data = await res.json();
    const batch = (data.candles || []).map((c) => ({
      time: Number(c.start),
      close: +c.close,
    }));
    if (!batch.length) break;
    out.push(...batch);
    const oldest = Math.min(...batch.map((b) => b.time));
    if (oldest >= cursor) break;
    cursor = oldest - 1;
    if (cursor <= start) break;
  }
  const map = new Map(out.map((c) => [c.time, c]));
  return [...map.values()].sort((a, b) => a.time - b.time);
}

function dayKey(unixSec) {
  return new Date(unixSec * 1000).toISOString().slice(0, 10);
}

function ensureDaily(state, unixSec) {
  const d = dayKey(unixSec);
  if (state.daily.date !== d) {
    state.daily = { date: d, pnlEur: 0, trades: 0, losses: 0 };
  }
}

function dailyBlocked(state) {
  if (state.daily.pnlEur <= -maxDailyLoss) return true;
  if (state.daily.trades >= maxDailyTrades) return true;
  return false;
}

function markToMarket(state, lastPrices) {
  let mtm = Number(state.paperCash.EUR) || 0;
  for (const [product, pos] of Object.entries(state.positions)) {
    const px = lastPrices[product];
    if (px != null && pos?.qty) mtm += Number(pos.qty) * px;
  }
  return mtm;
}

function feeAdjust(buyNotional, sellNotional) {
  return ((buyNotional + sellNotional) * (feePct / 100)) / 2;
}

async function runPaperSim({ strategy, products, cfgOverrides = {}, maxOpen = 4 }) {
  const cfg = { ...baseCfg, ...cfgOverrides, strategy };
  const state = {
    positions: {},
    paperCash: { EUR: seed },
    daily: { date: "", pnlEur: 0, trades: 0, losses: 0 },
  };
  let buyNotional = 0;
  let sellNotional = 0;

  const needHours = days * 24 + warmupHours + 24;
  const series = {};
  for (const product of products) {
    series[product] = await fetchHourly(product, needHours);
  }

  const simStart = Math.floor(Date.now() / 1000) - days * 24 * 3600;
  const timeline = [
    ...new Set(
      products.flatMap((p) =>
        series[p].filter((c) => c.time >= simStart).map((c) => c.time),
      ),
    ),
  ].sort((a, b) => a - b);

  const lastPrices = {};
  for (const t of timeline) {
    ensureDaily(state, t);
    const blocked = dailyBlocked(state);

    for (const product of products) {
      const candles = series[product];
      const idx = candles.findIndex((c) => c.time === t);
      if (idx < 0 || idx + 1 < lookback) continue;
      const window = candles.slice(Math.max(0, idx + 1 - lookback), idx + 1);
      const closes = window.map((c) => c.close);
      const price = candles[idx].close;
      lastPrices[product] = price;

      let position = state.positions[product] || null;
      if (position?.entry && price > (position.peakPrice || 0)) {
        position = { ...position, peakPrice: price };
        state.positions[product] = position;
      }

      const { signal, meta } = runStrategy(strategy, closes, cfg, position);
      if (!signal) continue;
      const isStop = String(meta.reason || "").startsWith("stop-loss");
      if (signal === "buy" && blocked) continue;
      if (signal === "sell" && !isStop && blocked) continue;

      if (signal === "buy") {
        if (position?.qty > 0) continue;
        if (Object.keys(state.positions).length >= maxOpen) continue;
        const have = Number(state.paperCash.EUR) || 0;
        const spendable = Math.max(0, have - reserve);
        if (spendable < minQuote) continue;
        const quoteSize = Number(Math.min(quoteSizeWant, spendable).toFixed(2));
        const fill = applyPaperFill(state, {
          signal: "buy",
          product,
          quote: "EUR",
          price,
          quoteSize,
        });
        if (!fill.ok) continue;
        buyNotional += fill.spent;
        continue;
      }

      if (signal === "sell" && position?.qty) {
        const frac = meta.sellFraction ?? 1;
        const baseSize = Number((Number(position.qty) * frac).toFixed(8));
        const fill = applyPaperFill(state, {
          signal: "sell",
          product,
          quote: "EUR",
          price,
          baseSize,
          position,
          frac,
          markScaledOut: Boolean(meta.markScaledOut),
        });
        if (!fill.ok) continue;
        sellNotional += fill.proceeds;
      }
    }
  }

  const equity = markToMarket(state, lastPrices);
  const cashDelta = equity - seed;
  const net = cashDelta - feeAdjust(buyNotional, sellNotional);
  return {
    strategy,
    label: cfgOverrides.label || strategy,
    products: products.join(","),
    equity: Number(equity.toFixed(2)),
    cashDelta: Number(cashDelta.toFixed(2)),
    netAfterFee: Number(net.toFixed(2)),
    returnPct: Number(((cashDelta / seed) * 100).toFixed(3)),
    netReturnPct: Number(((net / seed) * 100).toFixed(3)),
    openPositions: Object.keys(state.positions).length,
    trades: state.daily.trades,
  };
}

function buyHold(product, candles, simStart, simEnd) {
  const inWindow = candles.filter((c) => c.time >= simStart && c.time <= simEnd);
  if (inWindow.length < 2) return null;
  const p0 = inWindow[0].close;
  const p1 = inWindow.at(-1).close;
  const gross = ((p1 - p0) / p0) * seed;
  const fee = seed * (feePct / 100);
  return {
    label: `buy-hold ${product}`,
    equity: Number((seed + gross - fee).toFixed(2)),
    netAfterFee: Number((seed + gross - fee).toFixed(2)),
    returnPct: Number(((gross / seed) * 100).toFixed(3)),
    netReturnPct: Number((((gross - fee) / seed) * 100).toFixed(3)),
  };
}

const products4 = ["ETH-EUR", "BTC-EUR", "XRP-EUR", "SOL-EUR"];
const simStart = Math.floor(Date.now() / 1000) - days * 24 * 3600;
const simEnd = simStart + days * 24 * 3600;

console.log(`7-day comparison · seed €${seed} · window ending now\n`);

const rows = [];
rows.push({
  label: "cash (no trades)",
  netAfterFee: seed,
  netReturnPct: 0,
  note: "baseline",
});

const series = {};
for (const p of products4) {
  series[p] = await fetchHourly(p, days * 24 + warmupHours + 24);
  const bh = buyHold(p, series[p], simStart, simEnd);
  if (bh) rows.push({ ...bh, note: "passive" });
}

const strats = [
  ["pct-scalp", products4, {}],
  [
    "pct-scalp",
    ["BTC-EUR"],
    {
      label: "pct-scalp BTC only",
      stopLossPct: 2,
      takeProfitPct: 2.5,
      takeProfitPctMin: 2,
      takeProfitPctMax: 4,
      rsiBuyBelow: 28,
    },
  ],
  [
    "pct-scalp",
    products4,
    {
      label: "pct-scalp tight stop 1.5%",
      stopLossPct: 1.5,
      takeProfitPct: 2.5,
      takeProfitPctMin: 2,
      takeProfitPctMax: 4,
    },
  ],
  ["rsi-mean-reversion", products4, { rsiBuyBelow: 30, rsiSellAbove: 70 }],
  ["momentum", products4, { momentumThresholdPct: 2, momentumLookback: 12 }],
  ["sma-cross", products4, { smaFast: 9, smaSlow: 21 }],
];

for (const [name, prods, overrides] of strats) {
  const r = await runPaperSim({ strategy: name, products: prods, cfgOverrides: overrides });
  rows.push({ ...r, note: "active" });
}

rows.sort((a, b) => (b.netAfterFee ?? b.equity) - (a.netAfterFee ?? a.equity));

console.log("Ranked by net € (after fee est. where applicable):\n");
for (const r of rows) {
  const ret = r.netReturnPct ?? r.returnPct;
  console.log(
    `${String(r.label || r.strategy).padEnd(28)} €${String(r.netAfterFee ?? r.equity).padStart(8)} (${ret >= 0 ? "+" : ""}${ret}%)  ${r.note || ""}`,
  );
}
