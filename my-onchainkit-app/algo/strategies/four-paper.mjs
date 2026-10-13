/**
 * Four paper-only books driven by the Coinbase EUR tape on 2026-10-13.
 *
 * Daily candles (2026-06-16 → 2026-10-13) are still an uptrend: 90-day
 * gains about +26% (XRP) to +57% (AVAX), and five of the six pairs are
 * above the 50-day average. The last 7–14 days are a pullback, not a
 * breakout. 7-day daily returns: ADA −10.9%, LTC −10.4%, AVAX −10.8%,
 * XRP −8.1%, LINK −7.0%, BTC −2.0%. Daily RSI is 43–56 (not a washout).
 * Alts sit in the lower third of a wide 14-day range and 3.8–8.4% under
 * the 20-day average. BTC is the exception: about flat versus that
 * average, 14-day range only ~8%, 7-day ATR ~2.6%.
 *
 * ETH-EUR (−6.5% / 7d) and SOL-EUR (−10.9% / 7d) are the same pullback.
 * They are not a separate trend, so they stay out of the universe.
 *
 * Sells are not the ±1.5% bracket that lost the prior paper week, and
 * none of them targets a €0.50 scalp against a multi-euro stop.
 * Each function is long-only: buy to open, sell to close.
 */
import { sma } from "../lib/indicators.mjs";

export const FOUR_PAPER_STRATEGIES = Object.freeze([
  "trend-pullback",
  "sma-dip",
  "range-bounce",
  "quiet-leader",
]);

function asCandles(data) {
  if (!Array.isArray(data) || !data.length) return [];
  if (typeof data[0] === "number") {
    return data.map((close) => ({
      open: close,
      high: close,
      low: close,
      close,
    }));
  }
  return data.map((c) => {
    const close = Number(c.close);
    return {
      open: Number(c.open ?? close),
      high: Number(c.high ?? close),
      low: Number(c.low ?? close),
      close,
    };
  });
}

function smaAgo(closes, period, barsAgo) {
  if (!Number.isFinite(period) || period < 1) return null;
  if (!barsAgo) return sma(closes, period);
  if (closes.length < period + barsAgo) return null;
  return sma(closes.slice(0, -barsAgo), period);
}

function hold(meta, reason) {
  return { signal: null, meta: { ...meta, ready: true, reason } };
}

function buy(meta, reason, score) {
  return {
    signal: "buy",
    meta: { ...meta, ready: true, reason, score: Number(score) || 0 },
  };
}

function sell(meta, reason) {
  return {
    signal: "sell",
    meta: { ...meta, ready: true, reason, sellFraction: 1 },
  };
}

/**
 * Trend-follow. The 20-day average is still rising, but price has pulled
 * back. Buy the hourly reclaim of the 48-hour average only while price is
 * still within 4% of that rising 20-day average (a dip, not a breakdown
 * and not a chase).
 *
 * Sell is a chandelier, not a fixed 1.5% target. Hard stop is 5% (~€5 on
 * a €100 stake). The trail arms only after +8% and then gives back 2.5%
 * from the peak, so a trail that fires has locked more than the stop.
 */
export function trendPullback(data, cfg = {}, position = null) {
  const candles = asCandles(data);
  const closes = candles.map((c) => c.close);
  const slowN = cfg.smaSlow ?? 480;
  const fastN = cfg.smaFast ?? 48;
  const slopeBars = cfg.slopeBars ?? 24;
  const maxDiscountPct = cfg.maxDiscountPct ?? 4;
  const stopLossPct = cfg.stopLossPct ?? 5;
  const trailArmPct = cfg.trailArmPct ?? 8;
  const trailGivebackPct = cfg.trailGivebackPct ?? 2.5;
  const price = closes.length ? closes[closes.length - 1] : null;
  const slow = smaAgo(closes, slowN, 0);
  const slowPrev = smaAgo(closes, slowN, slopeBars);
  const fast = smaAgo(closes, fastN, 0);
  const prevFast = smaAgo(closes, fastN, 1);
  const prevClose = closes.length >= 2 ? closes[closes.length - 2] : null;

  const meta = {
    strategy: "trend-pullback",
    price: price != null ? Number(price.toFixed(6)) : null,
    smaSlow: slow != null ? Number(slow.toFixed(6)) : null,
    smaFast: fast != null ? Number(fast.toFixed(6)) : null,
    stopLossPct,
    trailArmPct,
    trailGivebackPct,
    inPosition: Boolean(position?.entry),
  };

  if (
    price == null ||
    slow == null ||
    slowPrev == null ||
    fast == null ||
    prevFast == null ||
    prevClose == null
  ) {
    return { signal: null, meta: { ...meta, ready: false, reason: "not enough history" } };
  }

  if (position?.entry) {
    const entry = Number(position.entry);
    const peak = Math.max(Number(position.peakPrice || entry), price);
    const pnlPct = ((price - entry) / entry) * 100;
    const peakPnlPct = ((peak - entry) / entry) * 100;
    const dropFromPeakPct = ((peak - price) / peak) * 100;
    meta.entry = entry;
    meta.pnlPct = Number(pnlPct.toFixed(3));
    meta.peakPrice = Number(peak.toFixed(6));
    meta.peakPnlPct = Number(peakPnlPct.toFixed(3));
    meta.dropFromPeakPct = Number(dropFromPeakPct.toFixed(3));

    if (pnlPct <= -stopLossPct) {
      return sell(meta, `trend-stop −${stopLossPct}%`);
    }
    if (peakPnlPct >= trailArmPct && dropFromPeakPct >= trailGivebackPct) {
      return sell(
        meta,
        `trend-trail −${trailGivebackPct}% from peak after +${trailArmPct}%`,
      );
    }
    return hold(meta, `holding trend (stop −${stopLossPct}%, arm +${trailArmPct}%)`);
  }

  const rising = slow >= slowPrev;
  const vsSlowPct = ((price - slow) / slow) * 100;
  const inBand = vsSlowPct >= -maxDiscountPct && vsSlowPct <= 1.5;
  const reclaimed = prevClose < prevFast && price >= fast;
  meta.vsSlowPct = Number(vsSlowPct.toFixed(2));
  meta.checks = { rising, inBand, reclaimed };

  if (rising && inBand && reclaimed) {
    return buy(meta, "reclaim of 48h average inside a rising 20d trend", vsSlowPct);
  }
  return hold(meta, "no trend-pullback reclaim");
}

/**
 * Dip buy. Alts are several percent under a 20-day average that is still
 * rising; daily RSI is not below 30, so the dip is the distance to that
 * average. BTC never reached a 5% discount on this tape, so it usually
 * will not qualify.
 *
 * Buy on the cross to 5% or more below the rising average.
 * Sell when the discount has closed to 1.5% (the dip reverted — about a
 * 3.5%+ price move from the 5% line) or when it widens to 9% (the dip
 * failed). That is a trip back toward the average, not a ±1.5% bracket
 * around the fill.
 */
export function smaDip(data, cfg = {}, position = null) {
  const closes = asCandles(data).map((c) => c.close);
  const period = cfg.smaPeriod ?? 480;
  const slopeBars = cfg.slopeBars ?? 48;
  const entryDiscount = Math.max(3, Number(cfg.entryDiscountPct ?? 5));
  const exitDiscount = Math.min(
    entryDiscount - 1,
    Number(cfg.exitDiscountPct ?? 1.5),
  );
  const failDiscount = Math.max(
    entryDiscount + 3,
    Number(cfg.failDiscountPct ?? 9),
  );
  const price = closes.length ? closes[closes.length - 1] : null;
  const slow = smaAgo(closes, period, 0);
  const slowPrev = smaAgo(closes, period, slopeBars);
  const prevSlow = smaAgo(closes, period, 1);
  const prevClose = closes.length >= 2 ? closes[closes.length - 2] : null;

  const discountOf = (px, avg) => (avg ? ((avg - px) / avg) * 100 : null);
  const discount = discountOf(price, slow);
  const prevDiscount = discountOf(prevClose, prevSlow);

  const meta = {
    strategy: "sma-dip",
    price: price != null ? Number(price.toFixed(6)) : null,
    sma: slow != null ? Number(slow.toFixed(6)) : null,
    discountPct: discount != null ? Number(discount.toFixed(2)) : null,
    entryDiscountPct: entryDiscount,
    exitDiscountPct: exitDiscount,
    failDiscountPct: failDiscount,
    inPosition: Boolean(position?.entry),
  };

  if (
    price == null ||
    slow == null ||
    slowPrev == null ||
    prevSlow == null ||
    prevClose == null ||
    discount == null ||
    prevDiscount == null
  ) {
    return { signal: null, meta: { ...meta, ready: false, reason: "not enough history" } };
  }

  if (position?.entry) {
    const entry = Number(position.entry);
    const pnlPct = ((price - entry) / entry) * 100;
    meta.entry = entry;
    meta.pnlPct = Number(pnlPct.toFixed(3));
    if (discount <= exitDiscount) {
      return sell(meta, `dip reverted to ${exitDiscount}% under the 20d average`);
    }
    if (discount >= failDiscount) {
      return sell(meta, `dip failed at ${failDiscount}% under the 20d average`);
    }
    return hold(meta, `holding dip (${discount.toFixed(1)}% under the 20d average)`);
  }

  const rising = slow >= slowPrev;
  const crossed = prevDiscount < entryDiscount && discount >= entryDiscount;
  meta.checks = { rising, crossed };
  if (rising && crossed) {
    return buy(
      meta,
      `cross to ${entryDiscount}% under a rising 20d average`,
      discount,
    );
  }
  return hold(meta, "no 20d-average dip cross");
}

function rangeStats(candles, bars) {
  if (candles.length < bars + 1) return null;
  const window = candles.slice(-bars - 1, -1);
  if (window.length < bars) return null;
  let hi = -Infinity;
  let lo = Infinity;
  for (const bar of window) {
    if (bar.high > hi) hi = bar.high;
    if (bar.low < lo) lo = bar.low;
  }
  if (!(hi > lo) || !Number.isFinite(hi) || !Number.isFinite(lo)) return null;
  const price = candles[candles.length - 1].close;
  return {
    hi,
    lo,
    mid: (hi + lo) / 2,
    pos: (price - lo) / (hi - lo),
    widthPct: ((hi - lo) / price) * 100,
    price,
  };
}

/**
 * Mean reversion. 72-hour ranges on the alts are wide (often 8–15%) and
 * price has been living in the lower part of that range. This is not an
 * RSI-30 scalp and not a breakout: 48-hour highs barely broke this week.
 *
 * Buy an up-close that turns a down-close, only in the bottom quarter of
 * the prior 72-hour range, and only when that range is at least 6% wide
 * so the path back to the midpoint is a real move (about 1.5% or more
 * even at the shallowest entry).
 *
 * Sell at the midpoint of the current prior-72h range, or if the close
 * undercuts that range low (the range broke). Distance to the midpoint is
 * at least the distance to the low when the entry is in the bottom quarter.
 */
export function rangeBounce(data, cfg = {}, position = null) {
  const candles = asCandles(data);
  const bars = cfg.rangeBars ?? 72;
  const maxPos = cfg.maxPos ?? 0.25;
  const minWidthPct = cfg.minWidthPct ?? 6;
  const maxWidthPct = cfg.maxWidthPct ?? 30;
  const stats = rangeStats(candles, bars);
  const price = candles.length ? candles[candles.length - 1].close : null;

  const meta = {
    strategy: "range-bounce",
    price: price != null ? Number(price.toFixed(6)) : null,
    rangePos: stats ? Number(stats.pos.toFixed(3)) : null,
    rangeWidthPct: stats ? Number(stats.widthPct.toFixed(2)) : null,
    rangeMid: stats ? Number(stats.mid.toFixed(6)) : null,
    inPosition: Boolean(position?.entry),
  };

  if (!stats || candles.length < bars + 3) {
    return { signal: null, meta: { ...meta, ready: false, reason: "not enough history" } };
  }

  if (position?.entry) {
    const entry = Number(position.entry);
    const pnlPct = ((price - entry) / entry) * 100;
    meta.entry = entry;
    meta.pnlPct = Number(pnlPct.toFixed(3));
    if (price < stats.lo) {
      return sell(meta, "range broke: close under the prior 72h low");
    }
    if (price >= stats.mid) {
      return sell(meta, "reverted to the prior 72h midpoint");
    }
    return hold(meta, "holding for the 72h midpoint");
  }

  const c0 = candles[candles.length - 1].close;
  const c1 = candles[candles.length - 2].close;
  const c2 = candles[candles.length - 3].close;
  const turnUp = c0 > c1 && c1 <= c2;
  const inZone = stats.pos <= maxPos && stats.pos >= 0;
  const wideEnough = stats.widthPct >= minWidthPct && stats.widthPct <= maxWidthPct;
  meta.checks = { turnUp, inZone, wideEnough };
  if (turnUp && inZone && wideEnough) {
    return buy(meta, "up-turn in the bottom quarter of the 72h range", (maxPos - stats.pos) * 100);
  }
  return hold(meta, "no 72h-range turn");
}

function quietSnapshot(candles, cfg) {
  const bars = cfg.rangeBars ?? 72;
  const stats = rangeStats(candles, bars);
  const closes = candles.map((c) => c.close);
  const fastN = cfg.smaFast ?? 48;
  const slowN = cfg.smaSlow ?? 480;
  const fast = smaAgo(closes, fastN, 0);
  const fastPrev = smaAgo(closes, fastN, cfg.slopeBars ?? 6);
  const slow = smaAgo(closes, slowN, 0);
  if (!stats || fast == null || fastPrev == null || slow == null) return null;
  const price = closes[closes.length - 1];
  const maxWidthPct = cfg.maxWidthPct ?? 5.5;
  const vsSlowPct = ((price - slow) / slow) * 100;
  const ok =
    stats.widthPct <= maxWidthPct &&
    price > fast &&
    fast >= fastPrev &&
    vsSlowPct >= -1;
  return { ok, stats, fast, fastPrev, slow, price, vsSlowPct, maxWidthPct };
}

/**
 * Quiet-leader. BTC's 7-day ATR is about 2.6% and its 72-hour range spent
 * the week mostly under 5.5%, while it held the 20-day average. The alts'
 * 72-hour ranges ran about 8–15%. This book only buys that compressed hold.
 *
 * Sell when the 72-hour range expands through 6.5% (the quiet regime
 * ended), or the close finishes 1% under the 48-hour average. A 2.5%
 * take-profit matches BTC's measured 7-day ATR (~€2.50 on €100, not
 * €0.50). The stop is 2%, capped so it cannot exceed that target.
 */
export function quietLeader(data, cfg = {}, position = null) {
  const candles = asCandles(data);
  const takeProfitPct = Math.max(2, Number(cfg.takeProfitPct ?? 2.5));
  const stopLossPct = Math.min(
    takeProfitPct,
    Math.max(1, Number(cfg.stopLossPct ?? 2)),
  );
  const exitWidthPct = cfg.exitWidthPct ?? 6.5;
  const smaBreakPct = cfg.smaBreakPct ?? 1;
  const now = quietSnapshot(candles, cfg);
  const prev = quietSnapshot(candles.slice(0, -1), cfg);
  const price = candles.length ? candles[candles.length - 1].close : null;

  const meta = {
    strategy: "quiet-leader",
    price: price != null ? Number(price.toFixed(6)) : null,
    rangeWidthPct: now ? Number(now.stats.widthPct.toFixed(2)) : null,
    smaFast: now ? Number(now.fast.toFixed(6)) : null,
    takeProfitPct,
    stopLossPct,
    exitWidthPct,
    inPosition: Boolean(position?.entry),
  };

  if (!now || !prev || price == null) {
    return { signal: null, meta: { ...meta, ready: false, reason: "not enough history" } };
  }

  if (position?.entry) {
    const entry = Number(position.entry);
    const pnlPct = ((price - entry) / entry) * 100;
    const underFastPct = ((now.fast - price) / now.fast) * 100;
    meta.entry = entry;
    meta.pnlPct = Number(pnlPct.toFixed(3));
    meta.underFastPct = Number(underFastPct.toFixed(2));
    if (now.stats.widthPct >= exitWidthPct) {
      return sell(meta, `quiet regime broke: 72h range ${exitWidthPct}%`);
    }
    if (underFastPct >= smaBreakPct) {
      return sell(meta, `lost the 48h average by ${smaBreakPct}%`);
    }
    if (pnlPct >= takeProfitPct) {
      return sell(meta, `quiet target +${takeProfitPct}% (BTC 7d ATR)`);
    }
    if (pnlPct <= -stopLossPct) {
      return sell(meta, `quiet stop −${stopLossPct}%`);
    }
    return hold(meta, "holding the compressed bid");
  }

  if (now.ok && !prev.ok) {
    return buy(
      meta,
      "72h range compressed and price is holding a rising 48h average",
      now.maxWidthPct - now.stats.widthPct,
    );
  }
  return hold(meta, "not a fresh quiet-leader setup");
}

export function runFourPaperStrategy(name, data, cfg, position = null) {
  switch (name) {
    case "trend-pullback":
      return trendPullback(data, cfg, position);
    case "sma-dip":
      return smaDip(data, cfg, position);
    case "range-bounce":
      return rangeBounce(data, cfg, position);
    case "quiet-leader":
      return quietLeader(data, cfg, position);
    default:
      return null;
  }
}
