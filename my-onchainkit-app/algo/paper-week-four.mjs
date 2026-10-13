/**
 * Resumable paper week for the four EUR books.
 * Paper only. No live orders.
 *
 * Window is fixed to the calendar week already in progress:
 *   2026-10-12T00:00:00Z → 2026-10-19T00:00:00Z
 *
 * Resume (catch up through the latest closed hourly candle):
 *   node algo/paper-week-four.mjs
 *
 * Hourly poll until the window ends:
 *   node algo/paper-week-four.mjs --poll
 *
 * Start the week over (deletes the four books):
 *   node algo/paper-week-four.mjs --reset
 *
 * Each strategy is its own virtual book. State, journal, and the
 * comparison report are gitignored. A later run continues from the
 * last closed candle it already walked.
 */
import {
  closeSync,
  existsSync,
  openSync,
  readFileSync,
  renameSync,
  rmSync,
  unlinkSync,
  writeFileSync,
} from "fs";
import { dirname, join, resolve } from "path";
import { fileURLToPath, pathToFileURL } from "url";
import {
  emptyBookState,
  emptyByProduct,
  fetchHourlyRange,
  loadConfig,
  printTable,
  processTimestamp,
  selectProducts,
  summarizeBook,
} from "./paper-strategies.mjs";
import { FOUR_PAPER_STRATEGIES } from "./strategies/four-paper.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));

export const WINDOW_FROM = "2026-10-12T00:00:00.000Z";
export const WINDOW_TO = "2026-10-19T00:00:00.000Z";
const WINDOW_FROM_SEC = Math.floor(Date.parse(WINDOW_FROM) / 1000);
const WINDOW_TO_SEC = Math.floor(Date.parse(WINDOW_TO) / 1000);
const HOUR = 3600;
const WARMUP_HOURS = 600;
const WEEK_PRODUCTS = Object.freeze([
  "ADA-EUR",
  "LINK-EUR",
  "AVAX-EUR",
  "BTC-EUR",
  "LTC-EUR",
  "XRP-EUR",
]);

const lockPath = join(__dirname, "paper-week-four.lock");
const reportPath = join(__dirname, "paper-week-four-report.json");

function statePath(name) {
  return join(__dirname, `paper-week-four.${name}.state.json`);
}

function journalPath(name) {
  return join(__dirname, `paper-week-four.${name}.journal.jsonl`);
}

function isDirectRun() {
  const entry = process.argv[1];
  if (!entry) return false;
  return import.meta.url === pathToFileURL(resolve(entry)).href;
}

function sleep(ms) {
  return new Promise((resolveSleep) => setTimeout(resolveSleep, ms));
}

function iso(sec) {
  return new Date(sec * 1000).toISOString();
}

function writeJsonAtomic(filePath, value) {
  const tmp = `${filePath}.${process.pid}.tmp`;
  writeFileSync(tmp, `${JSON.stringify(value, null, 2)}\n`);
  renameSync(tmp, filePath);
}

function writeJournal(filePath, rows) {
  const tmp = `${filePath}.${process.pid}.tmp`;
  const body = rows.map((row) => JSON.stringify(row)).join("\n");
  writeFileSync(tmp, rows.length ? `${body}\n` : "");
  renameSync(tmp, filePath);
}

function resetFiles() {
  for (const name of FOUR_PAPER_STRATEGIES) {
    rmSync(statePath(name), { force: true });
    rmSync(journalPath(name), { force: true });
  }
  rmSync(reportPath, { force: true });
}

function strategySpecs(cfg) {
  const listed = Array.isArray(cfg.strategies) ? cfg.strategies : [];
  return FOUR_PAPER_STRATEGIES.map((name) => {
    const found = listed.find((s) => s && s.name === name) || { name };
    return { ...found, name };
  });
}

function bookFromConfig(cfg) {
  if (cfg.execute === true) {
    throw new Error(
      "paper-week-four does not send orders and refuses execute:true.",
    );
  }
  if (
    Number(cfg.paperCashEur) !== 500 ||
    Number(cfg.quoteSize) !== 100 ||
    Number(cfg.maxOpenPositions) !== 2
  ) {
    throw new Error(
      "This week is fixed at €500 seed, €100 stake, and max 2 open positions.",
    );
  }
  return {
    seed: 500,
    stake: 100,
    maxOpen: 2,
    minQuote: Number(cfg.minQuoteSize ?? 5),
    reserve: Number(cfg.cashReserve ?? 1),
    feePct: Number(cfg.feeBufferPct ?? 1),
  };
}

function freshDoc(name, scfg, products, book) {
  return {
    version: 1,
    strategy: name,
    paper: true,
    execute: false,
    window: { from: WINDOW_FROM, to: WINDOW_TO },
    products: [...products],
    book,
    scfg,
    createdAt: new Date().toISOString(),
    updatedAt: null,
    lastBarTime: null,
    lastPrices: {},
    runtime: emptyBookState(book.seed),
    byProduct: emptyByProduct(products),
    journal: [],
  };
}

function identity(doc) {
  return JSON.stringify({
    version: doc.version,
    strategy: doc.strategy,
    window: doc.window,
    products: doc.products,
    book: doc.book,
    scfg: doc.scfg,
  });
}

function loadDoc(name, expected) {
  const file = statePath(name);
  const journalFile = journalPath(name);
  if (!existsSync(file)) {
    if (existsSync(journalFile)) {
      throw new Error(
        `${journalFile} exists without its state file. Refusing to reset this book. Pass --reset to start the week over.`,
      );
    }
    return null;
  }
  let doc;
  try {
    doc = JSON.parse(readFileSync(file, "utf8"));
  } catch (err) {
    throw new Error(
      `Cannot read ${file}: ${err.message}. Pass --reset to start the week over.`,
    );
  }
  if (identity(doc) !== identity(expected)) {
    throw new Error(
      `${file} is a different week or config. Pass --reset to discard the four books and start this week over.`,
    );
  }
  if (!doc.runtime || !Array.isArray(doc.journal) || !doc.byProduct) {
    throw new Error(
      `${file} is missing the book. Pass --reset to start the week over.`,
    );
  }
  writeJournal(journalFile, doc.journal);
  return doc;
}

function listOpen(runtime, lastPrices) {
  const rows = [];
  for (const [product, pos] of Object.entries(runtime.positions || {})) {
    if (!pos || !(Number(pos.qty) > 0)) continue;
    const last = lastPrices[product] ?? null;
    const entry = Number(pos.entry);
    const qty = Number(pos.qty);
    const unrealizedEur =
      last != null ? Number(((last - entry) * qty).toFixed(4)) : null;
    rows.push({
      product,
      qty,
      entry,
      last,
      unrealizedEur,
      openedAt: pos.openedAt || null,
    });
  }
  rows.sort((a, b) => a.product.localeCompare(b.product));
  return rows;
}

function fmtNum(n) {
  if (n == null || !Number.isFinite(Number(n))) return "-";
  const v = Number(n);
  if (Math.abs(v) >= 100) return v.toFixed(2);
  if (Math.abs(v) >= 1) return v.toFixed(4);
  return v.toFixed(6);
}

function pidAlive(pid) {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (err) {
    return err.code === "EPERM";
  }
}

function lockStale() {
  try {
    const txt = readFileSync(lockPath, "utf8");
    const [pidLine, stamp] = txt.split("\n");
    const pid = Number(pidLine);
    if (!pidAlive(pid)) return true;
    const age = Date.now() - Date.parse(stamp || "");
    return Number.isFinite(age) && age > 20 * 60 * 1000;
  } catch {
    return true;
  }
}

async function acquireLock() {
  const deadline = Date.now() + 120_000;
  for (;;) {
    try {
      const fd = openSync(lockPath, "wx");
      writeFileSync(fd, `${process.pid}\n${new Date().toISOString()}\n`);
      closeSync(fd);
      return;
    } catch (err) {
      if (err.code !== "EEXIST") throw err;
      if (lockStale()) {
        try {
          unlinkSync(lockPath);
        } catch {
          /* another run removed it */
        }
        continue;
      }
      if (Date.now() > deadline) {
        throw new Error("Another paper-week-four run holds the lock.");
      }
      await sleep(2000);
    }
  }
}

function releaseLock() {
  try {
    const txt = readFileSync(lockPath, "utf8");
    if (Number(txt.split("\n")[0]) === process.pid) unlinkSync(lockPath);
  } catch {
    /* already released */
  }
}

async function withLock(fn) {
  await acquireLock();
  try {
    return await fn();
  } finally {
    releaseLock();
  }
}

export function weekComplete() {
  if (Date.now() < Date.parse(WINDOW_TO)) return false;
  const need = WINDOW_TO_SEC - HOUR;
  return FOUR_PAPER_STRATEGIES.every((name) => {
    try {
      const doc = JSON.parse(readFileSync(statePath(name), "utf8"));
      return Number(doc.lastBarTime) >= need;
    } catch {
      return false;
    }
  });
}

export function msUntilNextPoll(now = Date.now()) {
  const grace = 90_000;
  const hour = HOUR * 1000;
  if (now >= Date.parse(WINDOW_TO)) return 60_000;
  const nextHour = Math.floor(now / hour) * hour + hour + grace;
  const finalWake = Date.parse(WINDOW_TO) + grace;
  return Math.max(5_000, Math.min(nextHour, finalWake) - now);
}

export async function catchUp({ reset = false } = {}) {
  if (reset) {
    console.log("Resetting the four paper books for this week.");
    resetFiles();
  }

  const { cfg, usedExample } = loadConfig();
  if (cfg.execute !== false || cfg.paper !== true) {
    console.log("Forcing paper=true, execute=false. This runner does not send orders.");
  }
  const book = bookFromConfig(cfg);
  const { products, dropped } = selectProducts(WEEK_PRODUCTS);
  if (products.join(",") !== WEEK_PRODUCTS.join(",")) {
    throw new Error(
      "This week stays on ADA-EUR, LINK-EUR, AVAX-EUR, BTC-EUR, LTC-EUR, XRP-EUR.",
    );
  }

  const nowSec = Math.floor(Date.now() / 1000);
  const fetchEnd = Math.min(nowSec, WINDOW_TO_SEC);
  const fetchStart = WINDOW_FROM_SEC - WARMUP_HOURS * HOUR;
  if (fetchEnd <= WINDOW_FROM_SEC) {
    throw new Error("This week has not started, so there is no closed candle to walk.");
  }

  const series = {};
  for (const product of products) {
    process.stdout.write(`Fetching ${product}… `);
    series[product] = await fetchHourlyRange(product, fetchStart, fetchEnd);
    console.log(`${series[product].length} bars`);
    const idx = series[product].findIndex((bar) => bar.time >= WINDOW_FROM_SEC);
    if (idx < 520) {
      throw new Error(
        `${product} has ${Math.max(idx, 0)} hourly bars before this week; need 520 so the 20-day average is ready.`,
      );
    }
  }

  const latestClosed = {};
  let openSkipped = null;
  for (const product of products) {
    let closed = null;
    for (const bar of series[product]) {
      if (bar.time < WINDOW_FROM_SEC || bar.time >= WINDOW_TO_SEC) continue;
      if (bar.time + HOUR <= nowSec) {
        closed = bar.time;
      } else if (openSkipped == null || bar.time > openSkipped) {
        openSkipped = bar.time;
      }
    }
    if (closed == null) {
      throw new Error(`${product} has no closed hourly candle inside this week yet.`);
    }
    latestClosed[product] = closed;
  }

  const syncedThrough = Math.min(...products.map((product) => latestClosed[product]));
  const times = new Set();
  for (const product of products) {
    for (const bar of series[product]) {
      if (bar.time >= WINDOW_FROM_SEC && bar.time <= syncedThrough) {
        times.add(bar.time);
      }
    }
  }
  const timeline = [...times].sort((a, b) => a - b);
  if (!timeline.length) {
    throw new Error("No closed hourly bars in this week yet.");
  }

  const specs = strategySpecs(cfg);
  const walked = [];
  for (const spec of specs) {
    const expected = freshDoc(spec.name, spec, products, book);
    const existing = loadDoc(spec.name, expected);
    const doc = existing || expected;
    const resumed = Boolean(existing);
    const cursor = Number.isFinite(doc.lastBarTime) ? doc.lastBarTime : WINDOW_FROM_SEC - 1;
    const todo = timeline.filter((t) => t > cursor);
    for (const t of todo) {
      const before = doc.journal.length;
      processTimestamp({
        name: spec.name,
        scfg: spec,
        products,
        series,
        t,
        state: doc.runtime,
        book,
        journal: doc.journal,
        byProduct: doc.byProduct,
        lastPrices: doc.lastPrices,
      });
      for (const row of doc.journal.slice(before)) {
        row.strategy = spec.name;
        if (row.event === "paper_buy" && doc.runtime.positions[row.product]) {
          doc.runtime.positions[row.product].openedAt = row.t;
        }
      }
    }
    if (todo.length) doc.lastBarTime = todo[todo.length - 1];
    doc.updatedAt = new Date().toISOString();
    walked.push({ doc, resumed, newSteps: todo.length });
  }

  for (const item of walked) {
    writeJsonAtomic(statePath(item.doc.strategy), item.doc);
    writeJournal(journalPath(item.doc.strategy), item.doc.journal);
  }

  const comparison = walked.map((item) => {
    const summary = summarizeBook({
      state: item.doc.runtime,
      journal: item.doc.journal,
      lastPrices: item.doc.lastPrices,
      book,
    });
    return {
      strategy: item.doc.strategy,
      summary,
      byProduct: item.doc.byProduct,
      lastBar: item.doc.lastBarTime ? iso(item.doc.lastBarTime) : null,
      newSteps: item.newSteps,
      resumed: item.resumed,
      open: listOpen(item.doc.runtime, item.doc.lastPrices),
    };
  });

  const report = {
    generatedAt: new Date().toISOString(),
    paper: true,
    execute: false,
    liveOrders: false,
    config: usedExample ? "paper-strategies.config.example.json" : "paper-strategies.config.json",
    window: { from: WINDOW_FROM, to: WINDOW_TO },
    closedThrough: iso(syncedThrough),
    openCandleSkipped: openSkipped ? iso(openSkipped) : null,
    latestClosedByProduct: Object.fromEntries(
      products.map((product) => [product, iso(latestClosed[product])]),
    ),
    hourlyStepsWalked: timeline.length,
    seedEur: book.seed,
    quoteSize: book.stake,
    maxOpenPositions: book.maxOpen,
    feeBufferPct: book.feePct,
    feeNote: "Fee estimate is not deducted from equity or realized.",
    products,
    dropped,
    resumed: comparison.every((row) => row.resumed),
    comparison: comparison.map((row) => ({
      strategy: row.strategy,
      ...row.summary,
      lastBar: row.lastBar,
      newSteps: row.newSteps,
      resumed: row.resumed,
      open: row.open,
    })),
    note: "Paper only. Equity is cash plus open marks at the last closed hourly candle walked. The forming hour is not used.",
  };
  writeJsonAtomic(reportPath, report);

  console.log(
    `\nPaper week · four books · seed €${book.seed} · stake €${book.stake} · max open ${book.maxOpen}`,
  );
  console.log(`Window ${WINDOW_FROM} → ${WINDOW_TO}`);
  console.log(
    `Closed candle reached ${report.closedThrough} (${timeline.length} hourly steps in the week so far)`,
  );
  if (openSkipped) {
    console.log(`Still-open candle not walked: ${iso(openSkipped)}`);
  }
  console.log(
    comparison
      .map((row) => `${row.strategy} ${row.resumed ? "resumed" : "new"} +${row.newSteps}h`)
      .join(" · "),
  );
  console.log("");
  printTable(comparison);
  console.log(
    "\nEquity is cash plus open marks at that closed candle. Realized is closed fills only. Buys and sells are the fills.",
  );
  console.log(
    `Fee estimate uses feeBufferPct ${book.feePct}% and is not deducted.`,
  );
  for (const row of comparison) {
    console.log(
      `${row.strategy}: fee≈€${row.summary.feeEstimateEur.toFixed(2)} cash €${row.summary.paperCashEur.toFixed(2)} last ${row.lastBar}`,
    );
    if (!row.open.length) {
      console.log("  open: flat");
    } else {
      for (const pos of row.open) {
        console.log(
          `  open ${pos.product} qty ${pos.qty} entry ${fmtNum(pos.entry)} last ${fmtNum(pos.last)} uPnL €${Number(pos.unrealizedEur).toFixed(2)} since ${pos.openedAt}`,
        );
      }
    }
    for (const product of products) {
      const item = row.byProduct[product];
      if (!item?.buys && !item?.sells) continue;
      const reasons = Object.entries(item.reasons)
        .map(([key, count]) => `${key}×${count}`)
        .join("; ");
      console.log(
        `  ${product}: buys ${item.buys} sells ${item.sells} pnl €${item.pnlEur.toFixed(2)} · ${reasons}`,
      );
    }
  }
  if (dropped.length) {
    for (const item of dropped) console.log(`dropped ${item.product}: ${item.reason}`);
  }
  console.log(`\nWrote ${reportPath}`);
  return report;
}

async function main() {
  const reset = process.argv.includes("--reset");
  const poll = process.argv.includes("--poll");
  if (!poll) {
    await withLock(() => catchUp({ reset }));
    return;
  }

  console.log(
    `paper-week-four poller · ${WINDOW_FROM} → ${WINDOW_TO} · paper only`,
  );
  let resetOnce = reset;
  for (;;) {
    try {
      await withLock(() => catchUp({ reset: resetOnce }));
      resetOnce = false;
    } catch (err) {
      console.error(`catch-up failed: ${err?.message || err}`);
    }
    if (weekComplete()) {
      console.log("Week window fully walked. Poller exiting.");
      return;
    }
    const waitMs = msUntilNextPoll();
    console.log(
      `Next catch-up in ${Math.round(waitMs / 1000)}s at ${new Date(Date.now() + waitMs).toISOString()}`,
    );
    await sleep(waitMs);
  }
}

if (isDirectRun()) {
  await main();
}
