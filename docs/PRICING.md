# Pricing profiles

Verified 2026-09-04 UTC against official catalogs.

## OpenAI API equivalent

`gpt-5.6-sol`: $4/M fresh input, $0.40/M cached input, $5/M cache-write input, and $20/M output. Requests with more than 272,000 input tokens apply 2× to all input categories and 1.5× to output for the entire request. Source: <https://developers.openai.com/api/docs/models/gpt-5.6-sol>.

## Vercel equivalent

`openai/gpt-5.6-sol`: $2/M input, $0.20/M cache read, $2.50/M cache write, and $10/M output, with the same 272,000 threshold, 2× input and 1.5× output tiers. Source: <https://ai-gateway.vercel.sh/v1/models> and <https://vercel.com/docs/ai-gateway/models-and-providers>.

The server refreshes this unauthenticated public catalog at startup and every 24 hours. A changed rule closes the old interval and creates a new row; failures retain the last rule and mark it stale. The UI labels this as “Vercel equivalent”, never as an actual Vercel bill.

## Codex credits equivalent

- Default `Plus/Pro Current`: 100 input, 10 cached input, 500 output credits/M; cache writes cost zero credits.
- `Plus/Pro Legacy 125`: 125 input, 12.5 cached input, 750 output credits/M, retained because it was supplied as the deployment's initial/migration profile.
- `Business/Enterprise Current`: stored separately at 100/10/500 and never silently applied to personal usage.
- `Manual`: a separate placeholder for administrator-defined rates.

Sources: <https://help.openai.com/en/articles/12642688-using-credits-for-flexible-usage-in-chatgpt-plus-pro> and <https://help.openai.com/en/articles/11481834-chatgpt-rate-card>.

Credits are local equivalents, not a claim about the OpenAI account ledger. While usage remains inside included plan allowance, incremental cash spend is shown as zero. Purchase batches supply the user's actual paid amount, fees and exchange rate for USD/CNY equivalents.
