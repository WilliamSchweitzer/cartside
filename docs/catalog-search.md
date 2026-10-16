# Catalog search evaluation

Search uses PostgreSQL 17 `pg_trgm`'s
[`word_similarity(query, text)`](https://www.postgresql.org/docs/17/pgtrgm.html).
The query is the first argument so a word or phrase can match an extent of a
longer product name or description. All literal matching remains case-insensitive
`strpos`, with no LIKE pattern interpolation. Queries containing `%`, `_`, or
backslash use literal matching only, because trigram extraction ignores punctuation.
Other queries of at least three characters add fuzzy candidates alongside literal
matches. One- and two-character queries use only literal matches in both fields.

Whitespace is trimmed and internal Unicode whitespace runs collapse to one space
at the API and repository boundaries. The API still validates the trimmed input
before collapsing: valid UTF-8, no NUL, at most 200 Unicode characters. Thus extra
internal whitespace cannot bypass the existing length limit.

Ordering is exact case-insensitive name, name prefix, other literal name, then
remaining candidates. Within a tier, descending `name_score + 0.2 * description_score`
favors name evidence; name and UUID break final ties. A description-only match
cannot pass a literal name match. Short-query scores are zero. Empty search skips
relevance and keeps the existing `name, id` alphabetical order. One filtered,
ordered SELECT applies LIMIT/OFFSET and fetches one lookahead row. There is no
union or join that could duplicate a product. The JSON contract and UI pagination
remain unchanged.

## Threshold calibration

The inclusive candidate cutoff is **0.44** on either field's word similarity.
Description evidence contributes only 20% to ranking, even when it admits a
candidate. These measurements use the actual 100 products from
`backend/internal/catalogseed/products.json`, loaded into isolated PostgreSQL
17.10, rather than invented grocery names:

| Query / product name | Name score | Cutoff 0.40 | Chosen 0.44 | Cutoff 0.45 / 0.60 |
| --- | ---: | --- | --- | --- |
| `brocoli` / Broccoli Crowns | 0.700 | Include | Include | Include / Include |
| `broccxli` / Broccoli Crowns | 0.556 | Include | Include | Include / Exclude |
| `brococli` / Broccoli Crowns | 0.444 | Include | Include | Exclude / Exclude |
| `brocoli` / Brown Rice | 0.375 | Exclude | Exclude | Exclude / Exclude |
| `broc` / Country White Bread | 0.400 | Include | Exclude | Exclude / Exclude |
| `butter` / Hamburger Buns | 0.429 | Include | Exclude | Exclude / Exclude |

0.44 retains the measured broccoli transposition while excluding the last two
unwanted candidates. This is a fixture-specific precision/recall compromise,
not a general typo-correction guarantee. Explicit comparisons avoid dependence on
`pg_trgm.word_similarity_threshold`; no session or transaction settings change.

## Evaluation cases and observed results

All cases below passed the explicit repository integration suite. “Exclude” means
absent from the complete result set, not merely below the first page. Listed
matches are selected assertions unless the row says “only.”

| Kind | Query | Expected matches / order, observed | Expected exclusions, observed |
| --- | --- | --- | --- |
| Exact name | `Gala Apples` | Gala Apples only | Bananas |
| Exact, case-insensitive | `whole milk` | Whole Milk first; Skim Milk is a weaker candidate | Bananas |
| Prefix plus fuzzy mixture | `broc` | Broccoli Crowns, Broccoli Florets, Brown Rice, Vegetable Broth, in that order | Country White Bread |
| Literal names plus fuzzy and description | `milk` | Reduced Fat Milk, Skim Milk, Whole Milk precede Buttermilk Waffles and Half and Half | Mixed Berries |
| Description-only literal | `guacamole` | Avocados only | Gala Apples |
| Description typo | `guacamle` | Avocados first | Gala Apples |
| Missing letter | `brocoli` | Broccoli Crowns, Broccoli Florets only | Brown Rice, Vegetable Broth |
| Substitution | `broccxli` | Broccoli Crowns, Broccoli Florets only | Brown Rice |
| Transposition | `brococli` | Broccoli Crowns, Broccoli Florets only | Brown Rice |
| Multiword transposition | `chikcen breast` | Chicken Breast only | Chicken Thighs |
| Multiword, whitespace and missing letter | `  whole<TAB>  mlk  ` | Whole Milk first, then Whole Chicken, Whole Wheat Bread, Brown Rice | Skim Milk |
| Two characters, literal only | `mi` | Mild Italian Sausage first; Mixed Berries and Skim Milk included | Gala Apples, Butter Croissants |
| One character, literal only | `m` | Mango Chunks first; Whole Milk included | Gala Apples |
| Short-word typo limitation | `aplpe` | No results | Gala Apples |
| Unrelated | `xylophone` | No results | Gala Apples |
| Literal punctuation | `%`, `_`, `%milk` | No results in fixture | Gala Apples; Whole Milk for `%milk` |
| Literal prefix tiers | `butter` | Butter Croissants, Buttermilk Waffles, Salted Butter, Unsalted Butter, Peanut Butter | Hamburger Buns |
| Empty / whitespace only | empty | Alphabetical browse with unchanged page metadata | No availability filtering |

Additional synthetic records test properties missing from this fixture: two exact
`Apple` names sort by UUID; exact names precede `Apple Pie` / `Applesauce`, which
precede `Crab Apple`, which precedes fuzzy `Appel` and description-only `Banana`.
`Appel` precedes `Banana`, demonstrating the lower description weight. One-row
pages reconstruct the full ordered result across every relevance tier and the
fixture cases, checking lookahead, stable ordering, and uniqueness. Synthetic
`Literal 50%_off` and `Literal 50xxoff` distinguish wildcard characters from SQL
patterns (including an embedded `%_off` query); backslashes and injection-shaped
input remain literal. Existing unavailable-product list/detail checks remain.

## Query plan and extension lifecycle

No index was added. After ANALYZE on 100 fixture rows,
`EXPLAIN (ANALYZE, BUFFERS)` for `brocoli` with a 21-row lookahead and zero offset
showed a sequential scan (98 rows removed), a two-row quicksort using 25 kB,
three shared buffer hits, and about **0.6 ms** execution in the local disposable
PostgreSQL 17.10 container. This is a small local measurement, not a latency
promise. The integration test logs the real plan on every run. A filter such as
`word_similarity(...) >= 0.44` does **not** automatically use a trigram index;
the documented index-supported operators and their plan behavior would need a
separate evaluation if the catalog grows.

Goose migration `00002_catalog_search.sql` installs `pg_trgm` with
`CREATE EXTENSION IF NOT EXISTS`. Applied migration 00001 is unchanged. Installation
requires the PostgreSQL extension files and database CREATE privilege (pg_trgm is
a trusted extension). Down deliberately retains pg_trgm, whether installed here
or already present, because it is database-wide and may be shared. Down does not
undo the deployed Go binary: roll the binary back too when reverting search.
An administrator may remove the extension separately after auditing dependents;
Cartside never issues DROP EXTENSION or CASCADE. Lifecycle integration tests cover
an already-installed extension, repeat Up, Down preserving products and extension,
and reapplication. Fresh catalog tests cover initial extension installation.

## Reproduction and verification (2026-10-15)

Use a dedicated PostgreSQL 17 container with fresh, disposable databases. Never
point these integration tests at the application database: they intentionally
insert/truncate test products and refuse databases containing user tables.
Run from `backend/`, assigning a different empty database URL to each command:

```sh
CARTSIDE_TEST_DATABASE_URL='postgres://test_user:disposable_password@127.0.0.1:TEST_PORT/catalog_test?sslmode=disable' \
  go test -tags=integration -race ./internal/catalog -run TestPostgresCatalog -count=1 -v
CARTSIDE_TEST_DATABASE_URL='postgres://test_user:disposable_password@127.0.0.1:TEST_PORT/lifecycle_test?sslmode=disable' \
  go test -tags=integration -race ./internal/catalogseed -run TestCatalogLifecycle -count=1 -v
```

Go formatting, vet, unit tests, race tests, build, and both explicit integration
suites passed. Authenticated headless Chrome requests through the real APISIX
session verified the typo examples, mixed literal/fuzzy results, description
ranking, page boundaries, empty search, and wildcard exclusions. The real UI
passed Enter search, Next, page reset, and Clear without frontend changes.
Unavailable rendering used an intercepted copy of a real API response with
`available: false`; the repository separately verified persisted unavailable
products. No stored product was changed for the UI check. Applying the additive
migration and restarting the local host API preserved a checksum of all product
data. Existing volumes and identity-provider configuration were preserved.

## Known limits

- Trigrams lose substantial evidence in short words. Some substitutions and
  transpositions fail (`aplpe` does not find Gala Apples); queries shorter than
  three characters have no fuzzy expansion at all.
- Continuous-extent matching can admit partial phrases: `whole mlk` also finds
  Whole Chicken, Whole Wheat Bread, and Brown Rice. No requirement forces every
  query word to match independently.
- Similar spelling is not intent: `milk` also admits Mild Italian Sausage and
  some descriptions containing “mild.” Name tiers protect strong literal results,
  but weaker results can be noisy. No synonyms or semantic interpretation exist.
- Matching is case-insensitive, without accent folding, stemming, spelling
  dictionaries, or another fuzzy extension. Punctuation is ignored by pg_trgm;
  wildcard/backslash queries therefore deliberately stay literal.
- OFFSET pages are deterministic for unchanged products. Concurrent catalog edits
  can shift later pages; there is no snapshot across separate HTTP requests.
