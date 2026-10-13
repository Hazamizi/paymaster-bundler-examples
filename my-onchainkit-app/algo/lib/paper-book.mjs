/**
 * Virtual cash helpers for the algo paper book.
 */

export function defaultPaperSeed(cfg, quote) {
  const q = String(quote || "EUR").toUpperCase();
  if (q === "EUR") {
    return Number(cfg.paperCashEur ?? cfg.paperStartingCash ?? 1000);
  }
  if (q === "USD" || q === "USDC") {
    return Number(
      cfg.paperCashUsd ?? cfg.paperCashEur ?? cfg.paperStartingCash ?? 1000,
    );
  }
  return Number(cfg.paperStartingCash ?? 1000);
}

/** Quote-agnostic daily realized PnL (keeps legacy `pnlEur` in sync). */
export function dailyPnl(daily) {
  if (!daily || typeof daily !== "object") return 0;
  const n = Number(daily.pnl ?? daily.pnlEur);
  return Number.isFinite(n) ? n : 0;
}

/** Mutates daily: keeps `pnl` and legacy `pnlEur` aligned. */
export function addDailyPnl(daily, delta) {
  const next = Number((dailyPnl(daily) + Number(delta)).toFixed(4));
  daily.pnl = next;
  daily.pnlEur = next; // legacy alias
  return next;
}

/**
 * Apply a simulated buy/sell to positions + daily (and optionally paperCash).
 * Mutates `state` in place; caller persists.
 *
 * @param {object} opts
 * @param {boolean} [opts.updateCash=true] Debit/credit `paperCash`. Set false for
 *   live dry-run (`execute=true` + TRADE_DRY_RUN) where sizing uses live balances.
 */
export function applyPaperFill(state, {
  signal,
  product,
  quote,
  price,
  quoteSize,
  baseSize,
  position,
  frac = 1,
  markScaledOut = false,
  rsi = null,
  reason = null,
  updateCash = true,
}) {
  const daily = state.daily || {
    date: "",
    pnl: 0,
    pnlEur: 0,
    trades: 0,
    losses: 0,
  };
  // Normalize legacy state that only has pnlEur
  if (daily.pnl == null && daily.pnlEur != null) daily.pnl = daily.pnlEur;
  if (daily.pnlEur == null && daily.pnl != null) daily.pnlEur = daily.pnl;
  state.daily = daily;
  state.paperCash = state.paperCash || {};
  const q = String(quote || "EUR").toUpperCase();
  if (updateCash && state.paperCash[q] == null) state.paperCash[q] = 0;

  if (signal === "buy") {
    const spent = Number(quoteSize);
    const px = Number(price);
    if (!(spent > 0) || !(px > 0)) {
      return { ok: false, reason: "invalid buy size/price" };
    }
    if (updateCash) {
      const have = Number(state.paperCash[q]) || 0;
      if (have + 1e-9 < spent) {
        return { ok: false, reason: `insufficient paper ${q}`, have, spent };
      }
      state.paperCash[q] = Number((have - spent).toFixed(2));
    }
    const qty = spent / px;
    state.positions[product] = {
      entry: px,
      qty,
      quoteSpent: spent,
      peakPrice: px,
      scaledOut: false,
      openedAt: new Date().toISOString(),
      entryRsi: rsi,
      paper: true,
    };
    daily.trades += 1;
    return {
      ok: true,
      event: "paper_buy",
      qty,
      spent,
      paperCash: updateCash ? state.paperCash[q] : undefined,
      updateCash,
    };
  }

  if (signal === "sell") {
    const soldQty = Number(baseSize);
    const px = Number(price);
    if (!(soldQty > 0) || !(px > 0) || !position?.qty) {
      return { ok: false, reason: "invalid sell" };
    }
    const proceeds = soldQty * px;
    const entry = Number(position.entry ?? px);
    const posQty = Number(position.qty);
    const costBasis =
      (Number(position.quoteSpent) || entry * posQty) * (soldQty / posQty);
    const pnl = proceeds - costBasis;
    const pnlPct = ((px - entry) / entry) * 100;

    if (updateCash) {
      state.paperCash[q] = Number(
        ((Number(state.paperCash[q]) || 0) + proceeds).toFixed(2),
      );
    }

    if (markScaledOut && frac < 1) {
      const remainFrac = 1 - frac;
      state.positions[product] = {
        ...position,
        qty: posQty * remainFrac,
        quoteSpent: Number(position.quoteSpent || 0) * remainFrac,
        scaledOut: true,
        peakPrice: Math.max(position.peakPrice || entry, px),
        paper: true,
      };
    } else {
      delete state.positions[product];
    }

    daily.trades += 1;
    addDailyPnl(daily, pnl);
    if (pnl < 0) daily.losses += 1;

    return {
      ok: true,
      event: markScaledOut ? "paper_scale_out" : "paper_sell",
      qty: soldQty,
      proceeds,
      entry,
      pnlPct: Number(pnlPct.toFixed(3)),
      pnl: Number(pnl.toFixed(4)),
      pnlEur: Number(pnl.toFixed(4)), // legacy
      paperCash: updateCash ? state.paperCash[q] : undefined,
      updateCash,
      reason,
    };
  }

  return { ok: false, reason: "unknown signal" };
}
