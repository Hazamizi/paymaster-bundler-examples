/**
 * Offline checks for paper-week-v2.
 * Confirms the RSI book, the ETH/SOL exclusion, the 1.5% full exit,
 * and that pct-scalp defaults are unchanged.
 *
 *   node agent/smoke-paper-week-v2.mjs
 */
import { readFileSync } from "fs";
import { dirname, join } from "path";
import { fileURLToPath } from "url";
import { applyPaperFill } from "../algo/lib/paper-book.mjs";
import { runStrategy } from "../algo/strategies/index.mjs";
import {
  PAPER_WEEK_V2_PRODUCTS,
  canPaperBuy,
  filterPaperWeekProducts,
  openPositionCount,
  paperWeekMaxOpen,
  paperWeekStake,
} from "../algo/paper-week-v2.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));

function assert(cond, message) {
  if (!cond) throw new Error(message);
}

function flat(price, n = 80) {
  return Array.from({ length: n }, () => price);
}

function decliningDip() {
  const closes = [];
  let price = 100;
  for (let i = 0; i < 60; i++) {
    price *= 0.999;
    closes.push(price);
  }
  for (let i = 0; i < 4; i++) {
    price *= 0.998;
    closes.push(price);
  }
  return closes;
}

// --- universe: ETH and SOL never trade, even if config lists them ---
{
  const { products, dropped } = filterPaperWeekProducts([
    "ETH-EUR",
    "sol-eur",
    "ADA-EUR",
    "BTC-USD",
    "DOGE-EUR",
    "ADA-EUR",
    "LINK-EUR",
  ]);
  assert(
    products.join(",") === "ADA-EUR,LINK-EUR",
    `filter kept ${products.join(",")}`,
  );
  const reasons = Object.fromEntries(dropped.map((d) => [d.product, d.reason]));
  assert(reasons["ETH-EUR"] === "hard-exclude ETH/SOL", "ETH must be hard-excluded");
  assert(reasons["SOL-EUR"] === "hard-exclude ETH/SOL", "SOL must be hard-excluded");
  assert(reasons["BTC-USD"] === "EUR quote only", "non-EUR must drop");
  assert(reasons["DOGE-EUR"] === "not in paper-week-v2 book", "DOGE must drop");
  const fallback = filterPaperWeekProducts(undefined);
  assert(
    fallback.products.join(",") === PAPER_WEEK_V2_PRODUCTS.join(","),
    "missing product list must use the EUR allowlist",
  );
  assert(
    !fallback.products.some((p) => p.startsWith("ETH") || p.startsWith("SOL")),
    "allowlist contains ETH or SOL",
  );
  console.log("OK: ETH/SOL and non-EUR quotes are excluded");
}

// --- book caps: €100 stake, max 2 open ---
{
  const stake = paperWeekStake(250);
  assert(stake.stake === 100 && stake.capped, `stake cap failed: ${stake.stake}`);
  assert(paperWeekStake(100).stake === 100, "€100 stake should pass through");
  assert(paperWeekStake(undefined).stake === 100, "missing stake should default to €100");
  const open = paperWeekMaxOpen(5);
  assert(open.maxOpen === 2 && open.capped, `max open cap failed: ${open.maxOpen}`);
  assert(paperWeekMaxOpen(2).maxOpen === 2, "max 2 should pass through");
  assert(
    canPaperBuy({
      openCount: 2,
      maxOpen: 2,
      hasPosition: false,
      spendable: 200,
      minQuote: 5,
      dailyBlocked: false,
    }) === false,
    "third position must be refused",
  );
  assert(
    canPaperBuy({
      openCount: 1,
      maxOpen: 2,
      hasPosition: false,
      spendable: 100,
      minQuote: 5,
      dailyBlocked: false,
    }) === true,
    "second position should be allowed",
  );
  assert(
    canPaperBuy({
      openCount: 0,
      maxOpen: 2,
      hasPosition: false,
      spendable: 4,
      minQuote: 5,
      dailyBlocked: false,
    }) === false,
    "below min quote must be refused",
  );

  const state = {
    positions: {},
    paperCash: { EUR: 500 },
    daily: { date: "2099-01-01", pnlEur: 0, trades: 0, losses: 0 },
  };
  for (const product of ["ADA-EUR", "LINK-EUR", "AVAX-EUR"]) {
    if (
      !canPaperBuy({
        openCount: openPositionCount(state.positions),
        maxOpen: 2,
        hasPosition: false,
        spendable: state.paperCash.EUR - 1,
        minQuote: 5,
        dailyBlocked: false,
      })
    ) {
      continue;
    }
    const fill = applyPaperFill(state, {
      signal: "buy",
      product,
      quote: "EUR",
      price: 1,
      quoteSize: 100,
    });
    assert(fill.ok, `paper buy failed for ${product}`);
  }
  assert(openPositionCount(state.positions) === 2, "book opened more than 2 lots");
  assert(!state.positions["AVAX-EUR"], "third buy was filled");
  assert(Math.abs(state.paperCash.EUR - 300) < 0.02, `cash after two stakes: ${state.paperCash.EUR}`);
  console.log("OK: stake €100 and max 2 open positions");
}

// --- entries and exits. No €0.50 scalp. pct-scalp defaults stay put. ---
{
  const dip = decliningDip();
  const buy = runStrategy("paper-week-v2", dip, { rsiBuyBelow: 32, feeBufferPct: 1 });
  assert(buy.meta.strategy === "paper-week-v2", "strategy name mismatch");
  assert(buy.signal === "buy", `expected RSI dip buy, got ${buy.signal} rsi=${buy.meta.rsi}`);
  assert(buy.meta.ready === true, "dip should be ready");
  assert(String(buy.meta.reason).includes("RSI"), `buy reason: ${buy.meta.reason}`);

  const thin = runStrategy("paper-week-v2", dip.slice(0, 10), {});
  assert(thin.signal == null, "short history must not buy");
  assert(thin.meta.ready === false, "short history should not be ready");
  assert(
    thin.meta.reason === "not enough candle history",
    `history reason: ${thin.meta.reason}`,
  );

  const flatBook = flat(100);
  const idle = runStrategy("paper-week-v2", flatBook, {});
  assert(idle.signal == null, "flat RSI must not buy");

  const scalpCfg = {
    takeProfitPct: 0.5,
    stopLossPct: 3,
    scaleOutPct: 0.5,
    feeBufferPct: 1,
  };
  const half = runStrategy(
    "paper-week-v2",
    flat(100.5),
    scalpCfg,
    { entry: 100, qty: 1, peakPrice: 100.5 },
  );
  assert(half.signal == null, `+0.5% must not exit, got ${half.signal} ${half.meta.reason}`);
  assert(half.meta.takeProfitPct === 1.5, `TP floor failed: ${half.meta.takeProfitPct}`);
  assert(half.meta.stopLossPct === 2, `SL cap failed: ${half.meta.stopLossPct}`);
  assert(!/0\.50|eur_0/i.test(String(half.meta.reason)), `scalp reason leaked: ${half.meta.reason}`);

  const winner = runStrategy(
    "paper-week-v2",
    flat(101.5),
    scalpCfg,
    { entry: 100, qty: 1, peakPrice: 101.5 },
  );
  assert(winner.signal === "sell", `+1.5% should take profit, got ${winner.meta.reason}`);
  assert(winner.meta.sellFraction === 1, "take-profit must be a full exit");
  assert(!winner.meta.markScaledOut, "take-profit must not scale out");
  assert(
    String(winner.meta.reason).startsWith("take-profit"),
    `winner reason: ${winner.meta.reason}`,
  );
  assert(!/eur_0\.50/i.test(String(winner.meta.reason)), "eur_0.50 exit returned");

  const stopped = runStrategy(
    "paper-week-v2",
    flat(98.5),
    { stopLossPct: 1.5, takeProfitPct: 1.5 },
    { entry: 100, qty: 1, peakPrice: 100 },
  );
  assert(stopped.signal === "sell", "-1.5% should stop");
  assert(stopped.meta.sellFraction === 1, "stop must be a full exit");
  assert(
    String(stopped.meta.reason).startsWith("stop-loss"),
    `stop reason: ${stopped.meta.reason}`,
  );

  const wideStopHolds = runStrategy(
    "paper-week-v2",
    flat(98.5),
    { stopLossPct: 3 },
    { entry: 100, qty: 1, peakPrice: 100 },
  );
  assert(
    wideStopHolds.signal == null,
    "a configured 3% stop must be capped, so -1.5% is not that stop",
  );
  const cappedStop = runStrategy(
    "paper-week-v2",
    flat(98),
    { stopLossPct: 3 },
    { entry: 100, qty: 1, peakPrice: 100 },
  );
  assert(cappedStop.signal === "sell", "-2% should hit the capped stop");
  assert(cappedStop.meta.stopLossPct === 2, "stop cap should report 2%");

  console.log("OK: paper-week-v2 dip, 1.5% full exit, no €0.50 scalp");
}

// --- pct-scalp defaults still belong to the old bot ---
{
  const up = flat(100);
  up[up.length - 1] = 101.5;
  const hold = runStrategy("pct-scalp", up, {}, { entry: 100, qty: 1, peakPrice: 101.5 });
  assert(hold.meta.strategy === "pct-scalp", "default path left pct-scalp");
  assert(hold.signal == null, "pct-scalp must not take a 1.5% winner");
  assert(hold.meta.stopLossPct === 3, `pct-scalp stop changed: ${hold.meta.stopLossPct}`);
  assert(hold.meta.takeProfitPct >= 3.5, `pct-scalp TP changed: ${hold.meta.takeProfitPct}`);

  const down = flat(100);
  down[down.length - 1] = 97;
  const oldStop = runStrategy("pct-scalp", down, {}, { entry: 100, qty: 1, peakPrice: 100 });
  assert(oldStop.signal === "sell", "pct-scalp 3% stop should still fire");
  assert(
    String(oldStop.meta.reason).includes("3%"),
    `pct-scalp stop reason changed: ${oldStop.meta.reason}`,
  );
  console.log("OK: pct-scalp defaults unchanged");
}

// --- example config matches the defended book ---
{
  const example = JSON.parse(
    readFileSync(join(__dirname, "../algo/paper-week-v2.config.example.json"), "utf8"),
  );
  assert(example.strategy === "paper-week-v2", "example strategy name");
  assert(example.paper === true && example.execute === false, "example must be paper-only");
  assert(example.quoteSize === 100, "example stake must be €100");
  assert(example.paperCashEur === 500, "example seed must be €500");
  assert(example.maxOpenPositions === 2, "example max open must be 2");
  assert(example.takeProfitPct === 1.5 && example.stopLossPct === 1.5, "example exits");
  assert(example.cashReserve === 1 && example.minQuoteSize === 5, "example cash bounds");
  assert(example.maxDailyLossEur === 15 && example.maxDailyTrades === 8, "example daily caps");
  assert(!example.scaleOutPct, "example must not define a scale-out");
  const listed = filterPaperWeekProducts(example.products);
  assert(listed.dropped.length === 0, "example lists a product outside the book");
  assert(listed.products.length === 6, "example should list the six EUR products");
  console.log("OK: paper-week-v2 example config");
}
