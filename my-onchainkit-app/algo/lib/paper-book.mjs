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

/**
 * Apply a simulated buy/sell to paperCash + positions + daily.
 * Mutates `state` in place; caller persists.
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
}) {
  const daily = state.daily || {
    date: "",
    pnlEur: 0,
    trades: 0,
    losses: 0,
  };
  state.daily = daily;
  state.paperCash = state.paperCash || {};
  const q = String(quote).toUpperCase();
  if (state.paperCash[q] == null) state.paperCash[q] = 0;

  if (signal === "buy") {
    const spent = Number(quoteSize);
    const px = Number(price);
    if (!(spent > 0) || !(px > 0)) {
      return { ok: false, reason: "invalid buy size/price" };
    }
    const have = Number(state.paperCash[q]) || 0;
    if (have + 1e-9 < spent) {
      return { ok: false, reason: `insufficient paper ${q}`, have, spent };
    }
    const qty = spent / px;
    state.paperCash[q] = Number((have - spent).toFixed(2));
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
      paperCash: state.paperCash[q],
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
    const pnlEur = proceeds - costBasis;
    const pnlPct = ((px - entry) / entry) * 100;

    state.paperCash[q] = Number(
      ((Number(state.paperCash[q]) || 0) + proceeds).toFixed(2),
    );

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
    daily.pnlEur = Number((daily.pnlEur + pnlEur).toFixed(4));
    if (pnlEur < 0) daily.losses += 1;

    return {
      ok: true,
      event: markScaledOut ? "paper_scale_out" : "paper_sell",
      qty: soldQty,
      proceeds,
      entry,
      pnlPct: Number(pnlPct.toFixed(3)),
      pnlEur: Number(pnlEur.toFixed(4)),
      paperCash: state.paperCash[q],
      reason,
    };
  }

  return { ok: false, reason: "unknown signal" };
}
