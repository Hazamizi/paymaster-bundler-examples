/**
 * 7-day paper-book test: walk pct-scalp (+ paper cash) over recent hourly candles.
 *
 *   node algo/paper-7day.mjs
 *   node algo/paper-7day.mjs --days 7
 *   node algo/paper-7day.mjs --days 1 --products ETH-EUR
 *   npm run paper:7day
 *
 * Uses public Coinbase market candles. No live orders.
 */
import { readFileSync, existsSync, writeFileSync, copyFileSync } from "fs";
import { dirname, join } from "path";
import { fileURLToPath } from "url";
import { applyPaperFill, defaultPaperSeed } from "./lib/paper-book.mjs";
import { runStrategy } from "./strategies/index.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const configPath = join(__dirname, "algo.config.json");
const examplePath = join(__dirname, "algo.config.example.json");
const outPath = join(__dirname, "paper-7day-report.json");

function argNum(flag, fallback) {
  const i = process.argv.indexOf(flag);
  if (i >= 0 && process.argv[i + 1]) return Number(process.argv[i + 1]);
  return fallback;
}

function argList(flag) {
  const i = process.argv.indexOf(flag);
  if (i < 0 || !process.argv[i + 1]) return null;
  return process.argv[i + 1]
    .split(",")
    .map((s) => s.trim().toUpperCase())
    .filter(Boolean);
}

const days = Math.max(1, argNum("--days", 7));
const warmupHours = Math.max(80, argNum("--warmup", 120));
const productOverride = argList("--products");

if (!existsSync(configPath) && existsSync(examplePath)) {
  copyFileSync(examplePath, configPath);
  console.log(`Copied algo.config.example.json → algo.config.json`);
}
if (!existsSync(configPath)) {
  console.error("Missing algo/algo.config.json");
  process.exit(1);
}

const cfg = JSON.parse(readFileSync(configPath, "utf8"));
const products = productOverride?.length
  ? productOverride
  : cfg.products || ["ETH-EUR"];
const lookback = Number(cfg.candleLookback ?? 120);
const feePct = Number(cfg.feeBufferPct ?? 1.0);
const quoteSizeWant = Number(cfg.quoteSize ?? 25);
const reserve = Number(cfg.cashReserve ?? 1);
const minQuote = Number(cfg.minQuoteSize ?? 5);
const maxDailyLoss = Math.abs(Number(cfg.maxDailyLossEur ?? 15));
const maxDailyTrades = Number(cfg.maxDailyTrades ?? 8);
const seed = defaultPaperSeed(cfg, "EUR");

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
    if (!res.ok) throw new Error(`candles ${product}: ${res.status}`);
    const data = await res.json();
    const batch = (data.candles || []).map((c) => ({
      time: Number(c.start),
      open: +c.open,
      high: +c.high,
      low: +c.low,
      close: +c.close,
      volume: +c.volume,
    }));
    if (!batch.length) break;
    out.push(...batch);
    const oldest = Math.min(...batch.map((b) => b.time));
    if (oldest >= cursor) break;
    cursor = oldest - 1;
    if (cursor <= start) break;
  }
  const map = new Map(out.map((c) => [c.time, c]));
  return [...map.values()]
    .filter((c) => Number.isFinite(c.close))
    .sort((a, b) => a.time - b.time);
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
  if (state.daily.pnlEur <= -maxDailyLoss) {
    return `daily loss cap €${maxDailyLoss}`;
  }
  if (state.daily.trades >= maxDailyTrades) {
    return `daily trade cap ${maxDailyTrades}`;
  }
  return null;
}

function markToMarket(state, lastPrices) {
  let mtm = Number(state.paperCash.EUR) || 0;
  for (const [product, pos] of Object.entries(state.positions)) {
    const px = lastPrices[product];
    if (px != null && pos?.qty) mtm += Number(pos.qty) * px;
  }
  return Number(mtm.toFixed(2));
}

const state = {
  positions: {},
  paperCash: { EUR: seed },
  daily: { date: "", pnlEur: 0, trades: 0, losses: 0 },
};

const journal = [];
const byProduct = Object.fromEntries(
  products.map((p) => [
    p,
    { buys: 0, sells: 0, scaleOuts: 0, pnlEur: 0, reasons: {} },
  ]),
);

const needHours = days * 24 + warmupHours + 24;
const series = {};
for (const product of products) {
  process.stdout.write(`Fetching ${product} (~${needHours}h)… `);
  series[product] = await fetchHourly(product, needHours);
  console.log(`${series[product].length} bars`);
}

const simStart = Math.floor(Date.now() / 1000) - days * 24 * 3600;
const allTimes = new Set();
for (const product of products) {
  for (const c of series[product]) {
    if (c.time >= simStart) allTimes.add(c.time);
  }
}
const timeline = [...allTimes].sort((a, b) => a - b);
if (!timeline.length) {
  console.error("No bars in the 7-day window");
  process.exit(1);
}

console.log(
  `\nPaper 7-day test · strategy=${cfg.strategy || "pct-scalp"} · seed €${seed}` +
    ` · quoteSize €${quoteSizeWant} · feeBuffer ${feePct}%` +
    `\nWindow: ${new Date(timeline[0] * 1000).toISOString()} → ${new Date(timeline.at(-1) * 1000).toISOString()}` +
    ` (${timeline.length} hourly steps)\n`,
);

const lastPrices = {};

for (const t of timeline) {
  ensureDaily(state, t);
  const blocked = dailyBlocked(state);

  for (const product of products) {
    const candles = series[product];
    const idx = candles.findIndex((c) => c.time === t);
    if (idx < 0) continue;
    if (idx + 1 < lookback) continue;

    const window = candles.slice(Math.max(0, idx + 1 - lookback), idx + 1);
    const closes = window.map((c) => c.close);
    const price = candles[idx].close;
    lastPrices[product] = price;

    let position = state.positions[product] || null;
    if (position?.entry && price > (position.peakPrice || 0)) {
      position = { ...position, peakPrice: price };
      state.positions[product] = position;
    }

    const { signal, meta } = runStrategy(
      cfg.strategy || "pct-scalp",
      closes,
      cfg,
      position,
    );
    if (!signal) continue;

    // Buys respect daily caps; stop-loss sells always allowed
    const isStop = String(meta.reason || "").startsWith("stop-loss");
    if (signal === "buy" && blocked) continue;
    if (signal === "sell" && !isStop && blocked) continue;

    if (signal === "buy") {
      if (position?.qty > 0) continue;
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
        rsi: meta.rsi,
        reason: meta.reason,
      });
      if (!fill.ok) continue;
      byProduct[product].buys += 1;
      byProduct[product].reasons[meta.reason || "buy"] =
        (byProduct[product].reasons[meta.reason || "buy"] || 0) + 1;
      journal.push({
        t: new Date(t * 1000).toISOString(),
        product,
        event: fill.event,
        price,
        qty: fill.qty,
        spent: fill.spent,
        paperCash: fill.paperCash,
        reason: meta.reason,
        rsi: meta.rsi,
      });
      continue;
    }

    if (signal === "sell") {
      if (!position?.qty) continue;
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
        rsi: meta.rsi,
        reason: meta.reason,
      });
      if (!fill.ok) continue;
      byProduct[product].sells += 1;
      if (fill.event === "paper_scale_out") byProduct[product].scaleOuts += 1;
      byProduct[product].pnlEur = Number(
        (byProduct[product].pnlEur + (fill.pnlEur || 0)).toFixed(4),
      );
      byProduct[product].reasons[meta.reason || "sell"] =
        (byProduct[product].reasons[meta.reason || "sell"] || 0) + 1;
      journal.push({
        t: new Date(t * 1000).toISOString(),
        product,
        event: fill.event,
        price,
        qty: fill.qty,
        proceeds: fill.proceeds,
        pnlEur: fill.pnlEur,
        pnlPct: fill.pnlPct,
        paperCash: fill.paperCash,
        reason: meta.reason,
        rsi: meta.rsi,
      });
    }
  }
}

const equity = markToMarket(state, lastPrices);
const realized = journal
  .filter((j) => j.pnlEur != null)
  .reduce((a, j) => a + j.pnlEur, 0);
const openMtm = equity - (Number(state.paperCash.EUR) || 0);
const cashDelta = equity - seed;
// Fee-aware estimate: subtract feeBuffer once per round-trip notional
const sellNotional = journal
  .filter((j) => j.event === "paper_sell" || j.event === "paper_scale_out")
  .reduce((a, j) => a + (j.proceeds || 0), 0);
const buyNotional = journal
  .filter((j) => j.event === "paper_buy")
  .reduce((a, j) => a + (j.spent || 0), 0);
const feeEst = ((buyNotional + sellNotional) * (feePct / 100)) / 2;
const netEst = cashDelta - feeEst;

const closed = journal.filter(
  (j) => j.event === "paper_sell" || j.event === "paper_scale_out",
);
const wins = closed.filter((j) => (j.pnlEur || 0) > 0).length;
const losses = closed.filter((j) => (j.pnlEur || 0) < 0).length;

const report = {
  generatedAt: new Date().toISOString(),
  days,
  strategy: cfg.strategy || "pct-scalp",
  products,
  seedEur: seed,
  quoteSize: quoteSizeWant,
  feeBufferPct: feePct,
  window: {
    from: new Date(timeline[0] * 1000).toISOString(),
    to: new Date(timeline.at(-1) * 1000).toISOString(),
    hourlySteps: timeline.length,
  },
  summary: {
    paperCashEur: Number(state.paperCash.EUR) || 0,
    equityEur: equity,
    cashDeltaEur: Number(cashDelta.toFixed(2)),
    realizedPnlEur: Number(realized.toFixed(4)),
    openMarkEur: Number(openMtm.toFixed(2)),
    feeEstimateEur: Number(feeEst.toFixed(2)),
    netAfterFeeEstEur: Number(netEst.toFixed(2)),
    returnPct: Number(((cashDelta / seed) * 100).toFixed(3)),
    netReturnPct: Number(((netEst / seed) * 100).toFixed(3)),
    buys: journal.filter((j) => j.event === "paper_buy").length,
    sells: closed.length,
    winSells: wins,
    lossSells: losses,
    openPositions: Object.keys(state.positions).length,
  },
  byProduct,
  openPositions: state.positions,
  journal,
};

writeFileSync(outPath, JSON.stringify(report, null, 2));

console.log("=== 7-day paper results ===");
console.log(
  `Equity €${equity.toFixed(2)} (cash €${(Number(state.paperCash.EUR) || 0).toFixed(2)}` +
    ` + open MTM €${openMtm.toFixed(2)})`,
);
console.log(
  `Δ vs seed: €${cashDelta.toFixed(2)} (${report.summary.returnPct}%)` +
    ` · realized €${realized.toFixed(2)}` +
    ` · fee≈€${feeEst.toFixed(2)} · net≈€${netEst.toFixed(2)} (${report.summary.netReturnPct}%)`,
);
console.log(
  `Fills: buys=${report.summary.buys} sells=${report.summary.sells}` +
    ` (W/L ${wins}/${losses}) · open lots=${report.summary.openPositions}`,
);
for (const product of products) {
  const row = byProduct[product];
  const reasons = Object.entries(row.reasons)
    .map(([k, v]) => `${k}×${v}`)
    .join(", ");
  console.log(
    `  ${product}: buys=${row.buys} sells=${row.sells}` +
      ` scaleOuts=${row.scaleOuts} pnl€=${row.pnlEur.toFixed(2)}` +
      (reasons ? ` · ${reasons}` : ""),
  );
}
if (Object.keys(state.positions).length) {
  console.log("Open positions:");
  for (const [product, pos] of Object.entries(state.positions)) {
    const px = lastPrices[product];
    const mtm = px != null ? pos.qty * px - (pos.quoteSpent || 0) : null;
    console.log(
      `  ${product}: qty=${pos.qty.toFixed(6)} entry=${pos.entry}` +
        (mtm != null ? ` uPnL€=${mtm.toFixed(2)}` : ""),
    );
  }
}
console.log(`\nWrote ${outPath}`);
