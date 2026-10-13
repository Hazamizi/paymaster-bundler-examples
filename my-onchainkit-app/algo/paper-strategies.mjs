/**
 * Paper comparison of the four EUR books in strategies/four-paper.mjs.
 * Virtual cash only. No live orders.
 *
 *   node algo/paper-strategies.mjs
 *   node algo/paper-strategies.mjs --days 7
 *   npm run paper:strategies
 *
 * Each strategy walks the same hourly candles with the same seed
 * (€500), stake (€100), and max open positions (2). Fills go through
 * applyPaperFill. The JSON report is gitignored.
 */
import { readFileSync, existsSync, writeFileSync } from "fs";
import { dirname, join } from "path";
import { fileURLToPath } from "url";
import { applyPaperFill } from "./lib/paper-book.mjs";
import { sma } from "./lib/indicators.mjs";
import { FOUR_PAPER_STRATEGIES } from "./strategies/four-paper.mjs";
import { runStrategy } from "./strategies/index.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const DEFAULT_PRODUCTS = Object.freeze([
  "ADA-EUR",
  "LINK-EUR",
  "AVAX-EUR",
  "BTC-EUR",
  "LTC-EUR",
  "XRP-EUR",
]);
const EXCLUDE_BASES = new Set(["ETH", "SOL"]);

function argNum(flag, fallback) {
  const i = process.argv.indexOf(flag);
  if (i >= 0 && process.argv[i + 1]) return Number(process.argv[i + 1]);
  return fallback;
}

function loadConfig() {
  const localPath = join(__dirname, "paper-strategies.config.json");
  const examplePath = join(__dirname, "paper-strategies.config.example.json");
  const configPath = existsSync(localPath) ? localPath : examplePath;
  if (!existsSync(configPath)) {
    throw new Error("Missing algo/paper-strategies.config.example.json");
  }
  return {
    cfg: JSON.parse(readFileSync(configPath, "utf8")),
    configPath,
    usedExample: configPath === examplePath,
  };
}

export function selectProducts(products) {
  const source =
    Array.isArray(products) && products.length ? products : DEFAULT_PRODUCTS;
  const kept = [];
  const dropped = [];
  const seen = new Set();
  for (const raw of source) {
    const product = String(raw || "").trim().toUpperCase();
    if (!product || seen.has(product)) continue;
    seen.add(product);
    const [base, quote] = product.split("-");
    if (EXCLUDE_BASES.has(base)) {
      dropped.push({
        product,
        reason:
          "ETH/SOL excluded: on 2026-10-13 they were in the same pullback as the book, with no separate trend",
      });
      continue;
    }
    if (quote !== "EUR") {
      dropped.push({ product, reason: "EUR quote only" });
      continue;
    }
    kept.push(product);
  }
  return { products: kept, dropped };
}

async function fetchHourly(product, hours) {
  const out = [];
  const end = Math.floor(Date.now() / 1000);
  const start = end - hours * 3600;
  let cursor = end;
  for (let page = 0; page < 20 && cursor > start; page++) {
    const pageStart = Math.max(start, cursor - 280 * 3600);
    const url = new URL(
      `https://api.coinbase.com/api/v3/brokerage/market/products/${product}/candles`,
    );
    url.searchParams.set("granularity", "ONE_HOUR");
    url.searchParams.set("start", String(pageStart));
    url.searchParams.set("end", String(cursor));
    url.searchParams.set("limit", "300");

    let data = null;
    let lastErr = null;
    for (let attempt = 1; attempt <= 4; attempt++) {
      try {
        const res = await fetch(url, { signal: AbortSignal.timeout(20000) });
        if (res.status === 429 || res.status >= 500) {
          lastErr = new Error(`Coinbase candles ${product}: HTTP ${res.status}`);
          await new Promise((r) => setTimeout(r, 400 * attempt));
          continue;
        }
        if (!res.ok) {
          const body = await res.text();
          throw new Error(
            `Coinbase candles ${product}: HTTP ${res.status} ${body.slice(0, 180)}`,
          );
        }
        data = await res.json();
        lastErr = null;
        break;
      } catch (err) {
        lastErr = err;
        if (attempt === 4) break;
        await new Promise((r) => setTimeout(r, 400 * attempt));
      }
    }
    if (!data) {
      throw new Error(
        `Coinbase candles ${product} failed: ${lastErr?.message || lastErr}`,
      );
    }

    const batch = (data.candles || [])
      .map((c) => ({
        time: Number(c.start),
        open: +c.open,
        high: +c.high,
        low: +c.low,
        close: +c.close,
        volume: +c.volume,
      }))
      .filter(
        (c) =>
          Number.isFinite(c.time) &&
          Number.isFinite(c.close) &&
          Number.isFinite(c.high) &&
          Number.isFinite(c.low) &&
          c.time >= start &&
          c.time <= cursor,
      );
    if (!batch.length) break;
    out.push(...batch);
    const oldest = Math.min(...batch.map((b) => b.time));
    if (oldest >= cursor) break;
    cursor = oldest - 1;
  }

  const map = new Map(out.map((c) => [c.time, c]));
  return [...map.values()].sort((a, b) => a.time - b.time);
}

function openCount(positions) {
  return Object.values(positions).filter((p) => p && Number(p.qty) > 0).length;
}

function markToMarket(state, lastPrices) {
  let mtm = Number(state.paperCash.EUR) || 0;
  for (const [product, pos] of Object.entries(state.positions)) {
    const px = lastPrices[product];
    if (px != null && pos?.qty) mtm += Number(pos.qty) * px;
  }
  return Number(mtm.toFixed(2));
}

function regimeRow(product, candles, simStart) {
  const inWindow = candles.filter((c) => c.time >= simStart);
  const last = candles[candles.length - 1];
  const first = inWindow[0] || last;
  const closes = candles.map((c) => c.close);
  const avg = sma(closes, 480);
  const prior = candles.slice(-73, -1);
  let width = null;
  if (prior.length >= 24 && last?.close) {
    const hi = Math.max(...prior.map((c) => c.high));
    const lo = Math.min(...prior.map((c) => c.low));
    width = ((hi - lo) / last.close) * 100;
  }
  const ret =
    first && last && first.close
      ? ((last.close - first.close) / first.close) * 100
      : null;
  return {
    product,
    last: last?.close ?? null,
    retPct: ret != null ? Number(ret.toFixed(2)) : null,
    vsSma20dPct:
      avg && last?.close ? Number((((last.close - avg) / avg) * 100).toFixed(2)) : null,
    width72hPct: width != null ? Number(width.toFixed(2)) : null,
    bars: candles.length,
  };
}

function walkStrategy({ name, scfg, products, series, timeline, book }) {
  const state = {
    positions: {},
    paperCash: { EUR: book.seed },
    daily: { date: "", pnlEur: 0, trades: 0, losses: 0 },
  };
  const journal = [];
  const byProduct = Object.fromEntries(
    products.map((p) => [p, { buys: 0, sells: 0, pnlEur: 0, reasons: {} }]),
  );
  const lastPrices = {};

  for (const t of timeline) {
    const sells = [];
    const buys = [];

    for (const product of products) {
      const candles = series[product];
      let idx = -1;
      for (let i = candles.length - 1; i >= 0; i--) {
        if (candles[i].time === t) {
          idx = i;
          break;
        }
        if (candles[i].time < t) break;
      }
      if (idx < 0) continue;
      const history = candles.slice(0, idx + 1);
      const price = history[history.length - 1].close;
      lastPrices[product] = price;

      let position = state.positions[product] || null;
      if (position?.entry && price > (position.peakPrice || 0)) {
        position = { ...position, peakPrice: price };
        state.positions[product] = position;
      }

      const { signal, meta } = runStrategy(name, history, scfg, position);
      if (signal === "sell" && position?.qty) {
        sells.push({ product, price, position, meta });
      } else if (signal === "buy" && !position?.qty) {
        buys.push({
          product,
          price,
          meta,
          score: Number(meta?.score) || 0,
        });
      }
    }

    for (const order of sells) {
      const position = state.positions[order.product];
      if (!position?.qty) continue;
      const frac = order.meta?.sellFraction ?? 1;
      const baseSize = Number((Number(position.qty) * frac).toFixed(8));
      const fill = applyPaperFill(state, {
        signal: "sell",
        product: order.product,
        quote: "EUR",
        price: order.price,
        baseSize,
        position,
        frac,
        rsi: order.meta?.rsi ?? null,
        reason: order.meta?.reason,
      });
      if (!fill.ok) continue;
      const row = byProduct[order.product];
      row.sells += 1;
      row.pnlEur = Number((row.pnlEur + (fill.pnlEur || 0)).toFixed(4));
      const reason = order.meta?.reason || "sell";
      row.reasons[reason] = (row.reasons[reason] || 0) + 1;
      journal.push({
        t: new Date(t * 1000).toISOString(),
        product: order.product,
        event: fill.event,
        price: order.price,
        qty: fill.qty,
        proceeds: fill.proceeds,
        pnlEur: fill.pnlEur,
        pnlPct: fill.pnlPct,
        paperCash: fill.paperCash,
        reason,
      });
    }

    buys.sort(
      (a, b) => b.score - a.score || a.product.localeCompare(b.product),
    );
    for (const order of buys) {
      if (state.positions[order.product]?.qty) continue;
      if (openCount(state.positions) >= book.maxOpen) break;
      const have = Number(state.paperCash.EUR) || 0;
      const spendable = Math.max(0, have - book.reserve);
      if (spendable < book.stake || spendable < book.minQuote) continue;
      const fill = applyPaperFill(state, {
        signal: "buy",
        product: order.product,
        quote: "EUR",
        price: order.price,
        quoteSize: book.stake,
        rsi: order.meta?.rsi ?? null,
        reason: order.meta?.reason,
      });
      if (!fill.ok) continue;
      const row = byProduct[order.product];
      row.buys += 1;
      const reason = order.meta?.reason || "buy";
      row.reasons[reason] = (row.reasons[reason] || 0) + 1;
      journal.push({
        t: new Date(t * 1000).toISOString(),
        product: order.product,
        event: fill.event,
        price: order.price,
        qty: fill.qty,
        spent: fill.spent,
        paperCash: fill.paperCash,
        reason,
      });
    }
  }

  const equity = markToMarket(state, lastPrices);
  const realized = journal
    .filter((j) => j.pnlEur != null)
    .reduce((sum, j) => sum + j.pnlEur, 0);
  const closed = journal.filter((j) => j.event === "paper_sell");
  const wins = closed.filter((j) => (j.pnlEur || 0) > 0).length;
  const losses = closed.filter((j) => (j.pnlEur || 0) < 0).length;
  const buyNotional = journal
    .filter((j) => j.event === "paper_buy")
    .reduce((sum, j) => sum + (j.spent || 0), 0);
  const sellNotional = closed.reduce((sum, j) => sum + (j.proceeds || 0), 0);
  const feeEst = ((buyNotional + sellNotional) * (book.feePct / 100)) / 2;

  return {
    strategy: name,
    summary: {
      paperCashEur: Number((Number(state.paperCash.EUR) || 0).toFixed(2)),
      equityEur: equity,
      realizedPnlEur: Number(realized.toFixed(4)),
      feeEstimateEur: Number(feeEst.toFixed(2)),
      buys: journal.filter((j) => j.event === "paper_buy").length,
      sells: closed.length,
      wins,
      losses,
      openPositions: openCount(state.positions),
    },
    byProduct,
    openPositions: state.positions,
    journal,
  };
}

function printTable(rows) {
  const header = [
    "strategy".padEnd(16),
    "equity".padStart(10),
    "realized".padStart(10),
    "buys".padStart(6),
    "sells".padStart(6),
    "W".padStart(4),
    "L".padStart(4),
    "open".padStart(5),
  ].join(" ");
  console.log(header);
  console.log("-".repeat(header.length));
  for (const row of rows) {
    const s = row.summary;
    console.log(
      [
        row.strategy.padEnd(16),
        s.equityEur.toFixed(2).padStart(10),
        s.realizedPnlEur.toFixed(2).padStart(10),
        String(s.buys).padStart(6),
        String(s.sells).padStart(6),
        String(s.wins).padStart(4),
        String(s.losses).padStart(4),
        String(s.openPositions).padStart(5),
      ].join(" "),
    );
  }
}

const days = Math.max(1, argNum("--days", 7));
const warmupHours = Math.max(520, argNum("--warmup", 600));
const { cfg, configPath, usedExample } = loadConfig();

if (cfg.execute !== false || cfg.paper !== true) {
  console.log("Forcing paper=true, execute=false. This runner does not send orders.");
}

const { products, dropped } = selectProducts(cfg.products);
if (!products.length) {
  console.error("No EUR products left to paper-trade.");
  process.exit(1);
}

const listed = Array.isArray(cfg.strategies) ? cfg.strategies : [];
const strategySpecs = FOUR_PAPER_STRATEGIES.map((name) => {
  const found = listed.find((s) => s && s.name === name) || { name };
  return { ...found, name };
});

const book = {
  seed: Number(cfg.paperCashEur ?? 500),
  stake: Number(cfg.quoteSize ?? 100),
  maxOpen: Math.max(1, Number(cfg.maxOpenPositions ?? 2)),
  minQuote: Number(cfg.minQuoteSize ?? 5),
  reserve: Number(cfg.cashReserve ?? 1),
  feePct: Number(cfg.feeBufferPct ?? 1),
};

const needHours = days * 24 + warmupHours;
const series = {};
for (const product of products) {
  process.stdout.write(`Fetching ${product} (~${needHours}h)… `);
  series[product] = await fetchHourly(product, needHours);
  console.log(`${series[product].length} bars`);
  if (series[product].length < 520) {
    throw new Error(
      `${product} returned ${series[product].length} hourly bars; need at least 520 for the 20-day average`,
    );
  }
}

const simStart = Math.floor(Date.now() / 1000) - days * 24 * 3600;
const times = new Set();
for (const product of products) {
  for (const bar of series[product]) {
    if (bar.time >= simStart) times.add(bar.time);
  }
}
const timeline = [...times].sort((a, b) => a - b);
if (!timeline.length) {
  console.error("No hourly bars in the test window.");
  process.exit(1);
}

console.log(
  `\nPaper strategies · ${usedExample ? "example config" : configPath}` +
    ` · seed €${book.seed} · stake €${book.stake} · max open ${book.maxOpen}` +
    `\nWindow ${new Date(timeline[0] * 1000).toISOString()} → ${new Date(timeline.at(-1) * 1000).toISOString()}` +
    ` (${timeline.length} hourly steps)\n`,
);

const regime = products.map((product) =>
  regimeRow(product, series[product], timeline[0]),
);
console.log("Measured at the end of this window (not a forecast):");
for (const row of regime) {
  console.log(
    `  ${row.product.padEnd(8)} last ${row.last}  window ${row.retPct}%` +
      `  vs 20d avg ${row.vsSma20dPct}%  72h width ${row.width72hPct}%`,
  );
}
if (dropped.length) {
  for (const item of dropped) console.log(`  dropped ${item.product}: ${item.reason}`);
}
console.log("");

const results = strategySpecs.map((spec) =>
  walkStrategy({
    name: spec.name,
    scfg: spec,
    products,
    series,
    timeline,
    book,
  }),
);

printTable(results);
console.log(
  `\nEquity is cash plus open marks. Realized is closed fills only. Fee estimate uses feeBufferPct ${book.feePct}% and is not deducted.`,
);
for (const row of results) {
  console.log(
    `${row.strategy}: fee≈€${row.summary.feeEstimateEur.toFixed(2)}` +
      ` cash €${row.summary.paperCashEur.toFixed(2)}`,
  );
  for (const product of products) {
    const item = row.byProduct[product];
    if (!item.buys && !item.sells) continue;
    const reasons = Object.entries(item.reasons)
      .map(([k, v]) => `${k}×${v}`)
      .join("; ");
    console.log(
      `  ${product}: buys ${item.buys} sells ${item.sells} pnl €${item.pnlEur.toFixed(2)} · ${reasons}`,
    );
  }
}

const outPath = join(__dirname, "paper-strategies-report.json");
const report = {
  generatedAt: new Date().toISOString(),
  paper: true,
  execute: false,
  days,
  products,
  dropped,
  seedEur: book.seed,
  quoteSize: book.stake,
  maxOpenPositions: book.maxOpen,
  feeBufferPct: book.feePct,
  config: usedExample ? "paper-strategies.config.example.json" : "paper-strategies.config.json",
  window: {
    from: new Date(timeline[0] * 1000).toISOString(),
    to: new Date(timeline.at(-1) * 1000).toISOString(),
    hourlySteps: timeline.length,
  },
  regime,
  comparison: results.map((row) => ({
    strategy: row.strategy,
    ...row.summary,
  })),
  strategies: results,
};
writeFileSync(outPath, JSON.stringify(report, null, 2));
console.log(`\nWrote ${outPath}`);
