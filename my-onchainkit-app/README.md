This is a [Next.js](https://nextjs.org) project bootstrapped with [`create-onchain`]().


## Getting Started

First, install dependencies:

```bash
npm install
# or
yarn install
# or
pnpm install
# or
bun install
```

Next, run the development server:

```bash
npm run dev
# or
yarn dev
# or
pnpm dev
# or
bun dev
```

Open [http://localhost:3000](http://localhost:3000) with your browser to see the result.

You can start editing the page by modifying `app/page.tsx`. The page auto-updates as you edit the file.


## Paper trading algo

Simulated fills with virtual cash (no live orders). Safe defaults live in `algo/algo.config.example.json` (`paper: true`, `execute: false`).

```bash
cp algo/algo.config.example.json algo/algo.config.json
npm run smoke:paper      # unit + forced buy/sell + daily caps
npm run algo:once        # one strategy tick
npm run paper:7day       # walk last 7 days of hourly candles
npm run algo             # poll forever in paper mode
```

Live trading requires `execute: true` **and** `TRADE_DRY_RUN=false` on the trade API. Seed cash with `paperCashEur` (default €1000).

## Learn More

To learn more about OnchainKit, see our [documentation](https://onchainkit.xyz/getting-started).

To learn more about Next.js, see the [Next.js documentation](https://nextjs.org/docs).
