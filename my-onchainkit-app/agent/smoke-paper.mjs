/**
 * Paper-book smoke: accounting unit check + one forced paper tick (public market).
 *
 *   node agent/smoke-paper.mjs
 */
import { writeFileSync, unlinkSync, existsSync, readFileSync } from "fs";
import { dirname, join } from "path";
import { fileURLToPath } from "url";
import { spawnSync } from "child_process";
import { applyPaperFill } from "../algo/lib/paper-book.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const root = join(__dirname, "..");
const algoDir = join(root, "algo");
const configPath = join(algoDir, "algo.config.json");
const statePath = join(algoDir, ".algo-state.json");
const journalPath = join(algoDir, "trades.journal.jsonl");

const hadConfig = existsSync(configPath);
const prevConfig = hadConfig ? readFileSync(configPath, "utf8") : null;
const hadState = existsSync(statePath);
const prevState = hadState ? readFileSync(statePath, "utf8") : null;
const hadJournal = existsSync(journalPath);
const prevJournal = hadJournal ? readFileSync(journalPath, "utf8") : null;

function restore() {
  if (hadConfig) writeFileSync(configPath, prevConfig);
  else if (existsSync(configPath)) unlinkSync(configPath);
  if (hadState) writeFileSync(statePath, prevState);
  else if (existsSync(statePath)) unlinkSync(statePath);
  if (hadJournal) writeFileSync(journalPath, prevJournal);
  else if (existsSync(journalPath)) unlinkSync(journalPath);
}

// --- unit: buy then sell restores cash and records PnL ---
{
  const state = {
    positions: {},
    paperCash: { EUR: 100 },
    daily: { date: "2099-01-01", pnlEur: 0, trades: 0, losses: 0 },
  };
  const buy = applyPaperFill(state, {
    signal: "buy",
    product: "ETH-EUR",
    quote: "EUR",
    price: 2000,
    quoteSize: 50,
  });
  if (!buy.ok || state.paperCash.EUR !== 50) {
    throw new Error(`unit buy failed: ${JSON.stringify(buy)} cash=${state.paperCash.EUR}`);
  }
  const pos = state.positions["ETH-EUR"];
  const sell = applyPaperFill(state, {
    signal: "sell",
    product: "ETH-EUR",
    quote: "EUR",
    price: 2200,
    baseSize: pos.qty,
    position: pos,
    frac: 1,
  });
  if (!sell.ok) throw new Error(`unit sell failed: ${JSON.stringify(sell)}`);
  // 100 - 50 spend + 55 proceeds (0.025 ETH * 2200) = 105
  if (Math.abs(state.paperCash.EUR - 105) > 0.02) {
    throw new Error(`expected cash≈105 after +10% sell, got ${state.paperCash.EUR}`);
  }
  if (!(state.daily.pnlEur > 0) || state.daily.trades !== 2) {
    throw new Error(`daily stats wrong: ${JSON.stringify(state.daily)}`);
  }
  console.log("OK: paper-book unit buy/sell", {
    cash: state.paperCash.EUR,
    pnlEur: state.daily.pnlEur,
    trades: state.daily.trades,
  });
}

const paperCfg = {
  strategy: "pct-scalp",
  products: ["ETH-EUR"],
  pollSec: 60,
  granularity: "ONE_HOUR",
  candleLookback: 120,
  rsiPeriod: 14,
  rsiBuyBelow: 32,
  rsiSellAbove: 65,
  maFast: 20,
  takeProfitPctMin: 3,
  takeProfitPct: 3.5,
  takeProfitPctMax: 5,
  stopLossPct: 3,
  preferLimit: false,
  maxSpreadPct: 99,
  skipWeekends: false,
  allowedHoursUtc: [],
  maxDailyLossEur: 50,
  maxDailyTrades: 20,
  quoteSize: 25,
  minQuoteSize: 5,
  cashReserve: 1,
  paperCashEur: 100,
  cooldownSec: 0,
  useCalibration: false,
  paper: true,
  execute: false,
};

try {
  writeFileSync(configPath, JSON.stringify(paperCfg, null, 2));
  if (existsSync(statePath)) unlinkSync(statePath);
  if (existsSync(journalPath)) unlinkSync(journalPath);

  const run = spawnSync(process.execPath, ["algo/run.mjs", "--once"], {
    cwd: root,
    encoding: "utf8",
    env: { ...process.env, ALGO_FORCE_SIGNAL: "buy" },
  });

  console.log(run.stdout || "");
  if (run.status !== 0) {
    console.error(run.stderr || "");
    throw new Error(`algo --once exited ${run.status}`);
  }

  const state = JSON.parse(readFileSync(statePath, "utf8"));
  const eth = state.positions?.["ETH-EUR"];
  const cash = Number(state.paperCash?.EUR);
  const trades = Number(state.daily?.trades || 0);

  if (!eth?.qty || !(eth.qty > 0)) {
    throw new Error("forced paper buy did not open ETH-EUR position");
  }
  const expectedCash = Number((100 - Number(eth.quoteSpent)).toFixed(2));
  if (Math.abs(cash - expectedCash) > 0.02) {
    throw new Error(
      `paper cash mismatch: have ${cash}, expected ~${expectedCash}`,
    );
  }
  if (trades < 1) throw new Error("daily.trades not incremented on paper buy");

  console.log(
    "OK: forced paper buy",
    JSON.stringify({
      qty: eth.qty,
      entry: eth.entry,
      quoteSpent: eth.quoteSpent,
      paperCashEur: cash,
      dailyTrades: trades,
    }),
  );

  // --- forced paper sell: close lot, credit cash, record PnL ---
  const entry = Number(eth.entry);
  const qty = Number(eth.qty);
  const quoteSpent = Number(eth.quoteSpent);
  const sellRun = spawnSync(process.execPath, ["algo/run.mjs", "--once"], {
    cwd: root,
    encoding: "utf8",
    env: { ...process.env, ALGO_FORCE_SIGNAL: "sell" },
  });
  console.log(sellRun.stdout || "");
  if (sellRun.status !== 0) {
    console.error(sellRun.stderr || "");
    throw new Error(`algo --once (sell) exited ${sellRun.status}`);
  }

  const after = JSON.parse(readFileSync(statePath, "utf8"));
  if (after.positions?.["ETH-EUR"]) {
    throw new Error("forced paper sell left ETH-EUR position open");
  }
  const cashAfter = Number(after.paperCash?.EUR);
  const tradesAfter = Number(after.daily?.trades || 0);
  const pnlAfter = Number(after.daily?.pnlEur || 0);
  if (tradesAfter < 2) {
    throw new Error(`expected ≥2 daily trades after sell, got ${tradesAfter}`);
  }
  // Proceeds ≈ qty * last ticker; allow market move between ticks.
  if (!(cashAfter > cash)) {
    throw new Error(
      `sell should credit cash (before=${cash}, after=${cashAfter})`,
    );
  }
  // Rough identity: cash ≈ seed - spent + proceeds; PnL ≈ cash - seed
  const seed = 100;
  const impliedPnl = cashAfter - seed;
  if (Math.abs(pnlAfter - impliedPnl) > 1) {
    throw new Error(
      `daily.pnlEur ${pnlAfter} vs cashΔ ${impliedPnl} (entry=${entry} qty=${qty} spent=${quoteSpent})`,
    );
  }
  console.log(
    "OK: forced paper sell",
    JSON.stringify({
      paperCashEur: cashAfter,
      dailyTrades: tradesAfter,
      dailyPnlEur: pnlAfter,
      cashDelta: impliedPnl,
    }),
  );
} finally {
  restore();
}
