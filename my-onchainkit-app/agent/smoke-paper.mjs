/**
 * Paper-book smoke: accounting unit check + one forced paper tick (public market).
 *
 *   node agent/smoke-paper.mjs
 */
import { writeFileSync, unlinkSync, existsSync, readFileSync } from "fs";
import { dirname, join } from "path";
import { fileURLToPath } from "url";
import { spawnSync } from "child_process";
import {
  applyPaperFill,
  defaultPaperSeed,
} from "../algo/lib/paper-book.mjs";

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

// --- unit: defaultPaperSeed honors config quote wallets ---
{
  if (defaultPaperSeed({ paperCashEur: 500 }, "EUR") !== 500) {
    throw new Error("paperCashEur seed failed");
  }
  if (defaultPaperSeed({ paperCashUsd: 200, paperCashEur: 500 }, "USD") !== 200) {
    throw new Error("paperCashUsd seed failed");
  }
  console.log("OK: defaultPaperSeed");
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

// --- unit: partial scale-out keeps half the lot and credits proceeds ---
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
    price: 1000,
    quoteSize: 40,
  });
  if (!buy.ok) throw new Error(`scale-out buy failed: ${JSON.stringify(buy)}`);
  const pos = state.positions["ETH-EUR"];
  const halfQty = pos.qty / 2;
  const scale = applyPaperFill(state, {
    signal: "sell",
    product: "ETH-EUR",
    quote: "EUR",
    price: 1100,
    baseSize: halfQty,
    position: pos,
    frac: 0.5,
    markScaledOut: true,
  });
  if (!scale.ok) throw new Error(`scale-out sell failed: ${JSON.stringify(scale)}`);
  const remain = state.positions["ETH-EUR"];
  if (!remain?.scaledOut || Math.abs(remain.qty - halfQty) > 1e-9) {
    throw new Error(`expected half lot remaining, got ${JSON.stringify(remain)}`);
  }
  // 60 cash + 22 proceeds (0.02 ETH @ 1100) = 82
  if (Math.abs(state.paperCash.EUR - 82) > 0.02) {
    throw new Error(`scale-out cash expected ~82, got ${state.paperCash.EUR}`);
  }
  console.log("OK: paper-book scale-out", {
    cash: state.paperCash.EUR,
    remainQty: remain.qty,
    pnlEur: state.daily.pnlEur,
  });
}

// --- unit: refuse buy when paper cash is insufficient ---
{
  const state = {
    positions: {},
    paperCash: { EUR: 10 },
    daily: { date: "2099-01-01", pnlEur: 0, trades: 0, losses: 0 },
  };
  const buy = applyPaperFill(state, {
    signal: "buy",
    product: "ETH-EUR",
    quote: "EUR",
    price: 2000,
    quoteSize: 50,
  });
  if (buy.ok || !String(buy.reason || "").includes("insufficient")) {
    throw new Error(`expected insufficient paper EUR: ${JSON.stringify(buy)}`);
  }
  console.log("OK: paper-book rejects oversize buy");
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

  // --- integration: daily loss cap blocks even forced paper buys ---
  {
    const today = new Date().toISOString().slice(0, 10);
    const capCfg = { ...paperCfg, maxDailyLossEur: 15 };
    writeFileSync(configPath, JSON.stringify(capCfg, null, 2));
    writeFileSync(
      statePath,
      JSON.stringify(
        {
          positions: {},
          paperCash: { EUR: 100 },
          daily: { date: today, pnlEur: -20, trades: 0, losses: 1 },
          cooldowns: {},
          pending: {},
        },
        null,
        2,
      ),
    );
    const capRun = spawnSync(process.execPath, ["algo/run.mjs", "--once"], {
      cwd: root,
      encoding: "utf8",
      env: { ...process.env, ALGO_FORCE_SIGNAL: "buy" },
    });
    const out = `${capRun.stdout || ""}${capRun.stderr || ""}`;
    if (capRun.status !== 0) {
      throw new Error(`daily cap smoke exited ${capRun.status}: ${out}`);
    }
    if (!out.includes("daily loss cap")) {
      throw new Error(`expected daily loss cap skip in output: ${out}`);
    }
    const capped = JSON.parse(readFileSync(statePath, "utf8"));
    if (capped.positions?.["ETH-EUR"]) {
      throw new Error("daily loss cap should not open a position");
    }
    console.log("OK: daily loss cap blocks forced paper buy");
    writeFileSync(configPath, JSON.stringify(paperCfg, null, 2));
    if (existsSync(statePath)) unlinkSync(statePath);
  }

  // --- integration: daily trade cap blocks forced paper buys ---
  {
    const today = new Date().toISOString().slice(0, 10);
    const capCfg = { ...paperCfg, maxDailyTrades: 3, maxDailyLossEur: 9999 };
    writeFileSync(configPath, JSON.stringify(capCfg, null, 2));
    writeFileSync(
      statePath,
      JSON.stringify(
        {
          positions: {},
          paperCash: { EUR: 100 },
          daily: { date: today, pnlEur: 0, trades: 3, losses: 0 },
          cooldowns: {},
          pending: {},
        },
        null,
        2,
      ),
    );
    const capRun = spawnSync(process.execPath, ["algo/run.mjs", "--once"], {
      cwd: root,
      encoding: "utf8",
      env: { ...process.env, ALGO_FORCE_SIGNAL: "buy" },
    });
    const out = `${capRun.stdout || ""}${capRun.stderr || ""}`;
    if (capRun.status !== 0) {
      throw new Error(`daily trade cap smoke exited ${capRun.status}: ${out}`);
    }
    if (!out.includes("daily trade cap")) {
      throw new Error(`expected daily trade cap skip in output: ${out}`);
    }
    const capped = JSON.parse(readFileSync(statePath, "utf8"));
    if (capped.positions?.["ETH-EUR"]) {
      throw new Error("daily trade cap should not open a position");
    }
    console.log("OK: daily trade cap blocks forced paper buy");
    writeFileSync(configPath, JSON.stringify(paperCfg, null, 2));
    if (existsSync(statePath)) unlinkSync(statePath);
  }

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
