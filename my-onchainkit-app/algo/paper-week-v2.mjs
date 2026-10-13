/**
 * paper-week-v2 — virtual weekly book. No live orders.
 *
 * The Oct 4–10 2026 Freqtrade paper week (€500 dry, €250 stake, max 2)
 * closed −€6.30 on 39 trades. 35 wins averaged +€0.73 via exit
 * `eur_0.50_to_1`. 4 stop-losses averaged −€7.93. Closed-fill PnL was
 * ETH/EUR −€8.89, SOL/EUR −€5.14, and small gains on XRP, LTC, BTC, AVAX,
 * LINK, and ADA. Dropping ETH and SOL on those same fills would have been
 * about +€7.72. There is no intra-trade price path, so this runner does
 * not pretend a tighter stop or a higher target was measured on that path.
 * It encodes the rule we can defend:
 *   - do not trade ETH or SOL, even if a config lists them
 *   - do not scalp ~€0.50 winners against multi-euro stops
 * Stake is €100 (capped). Take-profit and stop are about 1.5% (~€1.50 on
 * €100), full exit only, on the hourly close. Fills go through
 * applyPaperFill. A fee buffer is reported; it is not a live commission
 * and it does not send an order.
 *
 *   node algo/paper-week-v2.mjs
 *   node algo/paper-week-v2.mjs --days 7
 *   npm run paper:week
 */
import { readFileSync, existsSync, writeFileSync } from "fs";
import { dirname, join, resolve } from "path";
import { fileURLToPath } from "url";
import { applyPaperFill, defaultPaperSeed } from "./lib/paper-book.mjs";
import { paperWeekV2Levels, runStrategy } from "./strategies/index.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));

export const PAPER_WEEK_V2_PRODUCTS = Object.freeze([
  "ADA-EUR",
  "LINK-EUR",
  "AVAX-EUR",
  "BTC-EUR",
  "LTC-EUR",
  "XRP-EUR",
]);

const ALLOW = new Set(PAPER_WEEK_V2_PRODUCTS);
const HARD_EXCLUDE_BASES = new Set(["ETH", "SOL"]);

export function filterPaperWeekProducts(products) {
  const source =
    Array.isArray(products) && products.length ? products : PAPER_WEEK_V2_PRODUCTS;
  const kept = [];
  const dropped = [];
  const seen = new Set();
  for (const raw of source) {
    const product = String(raw || "").trim().toUpperCase();
    if (!product || seen.has(product)) continue;
    seen.add(product);
    const [base, quote] = product.split("-");
    let reason = null;
    if (HARD_EXCLUDE_BASES.has(base)) reason = "hard-exclude ETH/SOL";
    else if (quote !== "EUR") reason = "EUR quote only";
    else if (!ALLOW.has(product)) reason = "not in paper-week-v2 book";
    if (reason) dropped.push({ product, reason });
    else kept.push(product);
  }
  return { products: kept, dropped };
}

export function openPositionCount(positions) {
  return Object.values(positions || {}).filter((p) => p && Number(p.qty) > 0).length;
}

export function paperWeekStake(quoteSize) {
  const requested = Number(quoteSize);
  const stake = Math.min(
    100,
    Number.isFinite(requested) && requested > 0 ? requested : 100,
  );
  return {
    stake,
    capped: Number.isFinite(requested) && requested > stake,
  };
}

export function paperWeekMaxOpen(maxOpen) {
  const requested = Number(maxOpen);
  const max = Math.min(
    2,
    Math.max(1, Number.isFinite(requested) ? requested : 2),
  );
  return {
    maxOpen: max,
    capped: Number.isFinite(requested) && requested > max,
  };
}

export function canPaperBuy({
  openCount,
  maxOpen,
  hasPosition,
  spendable,
  minQuote,
  dailyBlocked,
}) {
  if (dailyBlocked) return false;
  if (hasPosition) return false;
  if (openCount >= maxOpen) return false;
  if (!(spendable >= minQuote)) return false;
  return true;
}

function argNum(flag, fallback) {
  const i = process.argv.indexOf(flag);
  if (i >= 0 && process.argv[i + 1]) return Number(process.argv[i + 1]);
  return fallback;
}

function loadConfig() {
  const localPath = join(__dirname, "paper-week-v2.config.json");
  const examplePath = join(__dirname, "paper-week-v2.config.example.json");
  const configPath = existsSync(localPath) ? localPath : examplePath;
  if (!existsSync(configPath)) {
    throw new Error(
      "Missing algo/paper-week-v2.config.example.json (and no local paper-week-v2.config.json)",
    );
  }
  const cfg = JSON.parse(readFileSync(configPath, "utf8"));
  return { cfg, configPath, usedExample: configPath === examplePath };
}

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
    let res;
    try {
      res = await fetch(url, { signal: AbortSignal.timeout(20000) });
    } catch (err) {
      throw new Error(
        `Coinbase candles ${product} failed: ${err?.message || err}`,
      );
    }
    if (!res.ok) {
      throw new Error(`Coinbase candles ${product}: HTTP ${res.status}`);
    }
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

function markToMarket(state, lastPrices) {
  let mtm = Number(state.paperCash.EUR) || 0;
  for (const [product, pos] of Object.entries(state.positions)) {
    const px = lastPrices[product];
    if (px != null && pos?.qty) mtm += Number(pos.qty) * px;
  }
  return Number(mtm.toFixed(2));
}

export async function runPaperWeek({ days = 7, warmupHours = 120 } = {}) {
  const { cfg, configPath, usedExample } = loadConfig();
  const { products, dropped } = filterPaperWeekProducts(cfg.products);
  if (!products.length) {
    throw new Error(
      "No paper-week-v2 products left after dropping ETH, SOL, and non-EUR quotes",
    );
  }

  // This runner never places an order. Flags in the report stay paper-only
  // even if a local config asks otherwise.
  if (cfg.execute !== false || cfg.paper !== true) {
    console.log(
      "Forcing paper=true, execute=false — paper-week-v2 does not send live orders.",
    );
  }

  const lookback = Number(cfg.candleLookback ?? 120);
  const feePct = Number(cfg.feeBufferPct ?? 1.0);
  const stake = paperWeekStake(cfg.quoteSize ?? 100);
  const openCap = paperWeekMaxOpen(cfg.maxOpenPositions ?? 2);
  const reserve = Number(cfg.cashReserve ?? 1);
  const minQuote = Number(cfg.minQuoteSize ?? 5);
  const maxDailyLoss = Math.abs(Number(cfg.maxDailyLossEur ?? 15));
  const maxDailyTrades = Number(cfg.maxDailyTrades ?? 8);
  const levels = paperWeekV2Levels(cfg);
  if (cfg.paperCashEur == null && cfg.paperStartingCash == null) {
    cfg.paperCashEur = 500;
  }
  const seed = defaultPaperSeed(cfg, "EUR");
  const strategyCfg = {
    ...cfg,
    strategy: "paper-week-v2",
    takeProfitPct: levels.takeProfitPct,
    stopLossPct: levels.stopLossPct,
    paper: true,
    execute: false,
  };

  const state = {
    positions: {},
    paperCash: { EUR: seed },
    daily: { date: "", pnlEur: 0, trades: 0, losses: 0 },
  };
  const journal = [];
  const byProduct = Object.fromEntries(
    products.map((p) => [p, { buys: 0, sells: 0, pnlEur: 0, reasons: {} }]),
  );

  function ensureDaily(unixSec) {
    const d = dayKey(unixSec);
    if (state.daily.date !== d) {
      state.daily = { date: d, pnlEur: 0, trades: 0, losses: 0 };
    }
  }

  function dailyBlocked() {
    if (state.daily.pnlEur <= -maxDailyLoss) return `daily loss cap €${maxDailyLoss}`;
    if (state.daily.trades >= maxDailyTrades) return `daily trade cap ${maxDailyTrades}`;
    return null;
  }

  const span = Math.max(1, Number(days) || 7);
  const warmup = Math.max(80, Number(warmupHours) || 120);
  const needHours = span * 24 + warmup + 24;
  const series = {};
  for (const product of products) {
    process.stdout.write(`Fetching ${product} (~${needHours}h)… `);
    series[product] = await fetchHourly(product, needHours);
    console.log(`${series[product].length} bars`);
  }

  const simStart = Math.floor(Date.now() / 1000) - span * 24 * 3600;
  const allTimes = new Set();
  for (const product of products) {
    for (const c of series[product]) {
      if (c.time >= simStart) allTimes.add(c.time);
    }
  }
  const timeline = [...allTimes].sort((a, b) => a - b);
  if (!timeline.length) {
    throw new Error("No hourly bars in the paper-week window");
  }

  console.log(
    `\nPaper week v2 · virtual · no live orders · strategy=paper-week-v2` +
      `\nConfig: ${usedExample ? "example" : "local"} ${configPath}` +
      `\nSeed €${seed} · stake €${stake.stake} · max open ${openCap.maxOpen}` +
      ` · TP ${levels.takeProfitPct}% · SL ${levels.stopLossPct}% · fee buffer ${feePct}%` +
      `\nWindow: ${new Date(timeline[0] * 1000).toISOString()} → ${new Date(timeline.at(-1) * 1000).toISOString()}` +
      ` (${timeline.length} hourly steps)\n`,
  );
  if (dropped.length) {
    console.log(
      "Excluded: " +
        dropped.map((d) => `${d.product} (${d.reason})`).join(", "),
    );
  }
  if (stake.capped) {
    console.log(`Stake capped at €${stake.stake} (config asked more).`);
  }
  if (openCap.capped) {
    console.log(`Max open positions capped at ${openCap.maxOpen}.`);
  }
  if (levels.takeProfitRaised || levels.stopCapped) {
    console.log(
      `Exits clamped to TP ${levels.takeProfitPct}% / SL ${levels.stopLossPct}%` +
        " (no €0.50 scalp, no ~3% stop).",
    );
  }

  const lastPrices = {};

  for (const t of timeline) {
    ensureDaily(t);
    const blocked = dailyBlocked();

    for (const product of products) {
      const candles = series[product];
      const idx = candles.findIndex((c) => c.time === t);
      if (idx < 0) continue;
      if (idx + 1 < lookback) continue;

      // Decisions use the hourly close only. High/low is not an intra-trade path.
      const window = candles.slice(Math.max(0, idx + 1 - lookback), idx + 1);
      const closes = window.map((c) => c.close);
      const price = candles[idx].close;
      lastPrices[product] = price;
      const position = state.positions[product] || null;

      const { signal, meta } = runStrategy(
        "paper-week-v2",
        closes,
        strategyCfg,
        position,
      );
      if (!signal) continue;

      const isStop = String(meta.reason || "").startsWith("stop-loss");
      if (signal === "buy" && blocked) continue;
      if (signal === "sell" && !isStop && blocked) continue;

      if (signal === "buy") {
        const have = Number(state.paperCash.EUR) || 0;
        const spendable = Math.max(0, have - reserve);
        if (
          !canPaperBuy({
            openCount: openPositionCount(state.positions),
            maxOpen: openCap.maxOpen,
            hasPosition: Boolean(position?.qty > 0),
            spendable,
            minQuote,
            dailyBlocked: Boolean(blocked),
          })
        ) {
          continue;
        }
        const quoteSize = Number(Math.min(stake.stake, spendable).toFixed(2));
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
        // Full exit only. Never a partial that could recreate a €0.50 scalp.
        const frac = 1;
        const baseSize = Number(position.qty);
        const fill = applyPaperFill(state, {
          signal: "sell",
          product,
          quote: "EUR",
          price,
          baseSize,
          position,
          frac,
          markScaledOut: false,
          rsi: meta.rsi,
          reason: meta.reason,
        });
        if (!fill.ok) continue;
        byProduct[product].sells += 1;
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
  const sellNotional = journal
    .filter((j) => j.event === "paper_sell")
    .reduce((a, j) => a + (j.proceeds || 0), 0);
  const buyNotional = journal
    .filter((j) => j.event === "paper_buy")
    .reduce((a, j) => a + (j.spent || 0), 0);
  const feeEst = ((buyNotional + sellNotional) * (feePct / 100)) / 2;
  const netEst = cashDelta - feeEst;
  const closed = journal.filter((j) => j.event === "paper_sell");
  const wins = closed.filter((j) => (j.pnlEur || 0) > 0).length;
  const losses = closed.filter((j) => (j.pnlEur || 0) < 0).length;

  const report = {
    generatedAt: new Date().toISOString(),
    days: span,
    strategy: "paper-week-v2",
    paper: true,
    execute: false,
    liveOrders: false,
    products,
    dropped,
    seedEur: seed,
    quoteSize: stake.stake,
    maxOpenPositions: openCap.maxOpen,
    takeProfitPct: levels.takeProfitPct,
    stopLossPct: levels.stopLossPct,
    feeBufferPct: feePct,
    cashReserve: reserve,
    minQuoteSize: minQuote,
    maxDailyLossEur: maxDailyLoss,
    maxDailyTrades,
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
      openPositions: openPositionCount(state.positions),
    },
    byProduct,
    openPositions: state.positions,
    journal,
  };

  const outPath = join(__dirname, "paper-week-v2-report.json");
  writeFileSync(outPath, JSON.stringify(report, null, 2));

  console.log("=== paper-week-v2 (virtual, no live orders) ===");
  console.log(
    `Seed €${seed.toFixed(2)} · equity €${equity.toFixed(2)}` +
      ` · realized €${realized.toFixed(2)}` +
      ` · fee≈€${feeEst.toFixed(2)} · net≈€${netEst.toFixed(2)}`,
  );
  console.log(
    `Fills: buys=${report.summary.buys} sells=${report.summary.sells}` +
      ` (W/L ${wins}/${losses}) · open=${report.summary.openPositions}`,
  );
  for (const product of products) {
    const row = byProduct[product];
    console.log(
      `  ${product}: buys=${row.buys} sells=${row.sells} pnl€=${row.pnlEur.toFixed(2)}`,
    );
  }
  const openNames = Object.keys(state.positions);
  if (!openNames.length) {
    console.log("Open positions: none");
  } else {
    console.log("Open positions:");
    for (const [product, pos] of Object.entries(state.positions)) {
      const px = lastPrices[product];
      const mtm = px != null ? pos.qty * px - (pos.quoteSpent || 0) : null;
      console.log(
        `  ${product}: qty=${Number(pos.qty).toFixed(6)} entry=${pos.entry}` +
          (mtm != null ? ` uPnL€=${mtm.toFixed(2)}` : ""),
      );
    }
  }
  console.log(`\nWrote ${outPath}`);
  return report;
}

function invokedDirectly() {
  const entry = process.argv[1];
  if (!entry) return false;
  return fileURLToPath(import.meta.url) === resolve(entry);
}

if (invokedDirectly()) {
  const days = Math.max(1, argNum("--days", 7));
  const warmupHours = Math.max(80, argNum("--warmup", 120));
  runPaperWeek({ days, warmupHours }).catch((err) => {
    console.error(`paper-week-v2 did not complete: ${err?.message || err}`);
    console.error("No live orders were sent.");
    process.exit(1);
  });
}
