# CLI Tools Reference for Agent & Pipeline Use

Best-of-breed Unix-style CLI tools optimized for **batch mode, scripting, and agent environments**.
All tools work non-interactively and compose well in pipelines.
TUI/interactive-only tools are excluded.

Each section lists tools ranked **① primary · ② secondary · ③ tertiary**.

---

## Core Unix Tooling

### Text Search & Processing

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **ripgrep** `rg` | Recursive regex search | `rg --json pattern` emits NDJSON with file/line/match — machine-readable search; 10-100× faster than grep | `brew install ripgrep` |
| ② | **sd** | Intuitive find-and-replace (`sed` alternative) | Cleaner regex syntax, multiline-aware; `sd 'old' 'new' file` vs arcane sed flags | `cargo install sd` |
| ③ | **choose** | Field selector (`cut`/`awk` alternative) | `choose 0 2` instead of `awk '{print $1,$3}'` — readable field slicing | `cargo install choose` |

### File System

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **fd** | `find` replacement | Sane defaults, respects `.gitignore`, `--exec` parallelism; `fd -e py --exec wc -l` | `brew install fd` |
| ② | **dust** | `du` replacement | Visual tree of disk usage sorted by size; `--json` for structured output | `cargo install du-dust` |
| ③ | **duf** | `df` replacement | Colorized, grouped by mount type; `--json` for structured disk usage data | `brew install duf` |

### System & Process Info

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **jc** | Converts command output to JSON | Parses 150+ commands (`ps`, `ls`, `dig`, `netstat`, `df`) into JSON — bridge any legacy tool to pipelines | `brew install jc` / `pip install jc` |
| ② | **procs** | `ps` replacement | `--json` output; tree view; keyword search; written in Rust | `cargo install procs` |
| ③ | **hyperfine** | Command benchmarking | `--export-json results.json` with warmup, stats, and side-by-side comparison | `cargo install hyperfine` |

### Parallelism & Workflow

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **GNU parallel** | Run shell commands in parallel | Turns any loop into parallel jobs; `--jobs`, `--eta`, `--results dir` for structured output | `brew install parallel` |
| ② | **entr** | Re-run commands on file change | `ls *.py \| entr pytest` — event-driven pipeline trigger; `-p` waits for initial change | `brew install entr` |
| ③ | **direnv** | Per-directory environment loading | `.envrc` auto-loaded/unloaded on `cd`; hooks into any shell; secrets per project | `brew install direnv` |

---

## Data Formats

### JSON

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **jq** | JSON processor | The gold standard; `curl api/data \| jq '.results[] \| select(.active)'` — indispensable | `brew install jq` |
| ② | **jo** | Creates JSON from shell args | `jo name=bob age=42 tags[]=a tags[]=b` → JSON without escaping nightmares | `brew install jo` |
| ③ | **fx** | JSON processor using JavaScript | `fx data.json '.filter(x => x.age > 30)'` — full JS for complex transforms | `npm install -g fx` |

### YAML / TOML / XML

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **yq** | YAML/JSON/TOML/XML processor | `yq eval '.spec.replicas = 3' -i deployment.yaml` — in-place edits across all formats | `brew install yq` |
| ② | **dasel** | Multi-format selector/modifier | Single tool for JSON/YAML/TOML/XML/CSV; xpath-like selectors; great for config patching | `brew install dasel` |
| ③ | **xq** | XML/HTML query tool | Like jq but for XML; `xq '.rss.channel.item[].title'` — structured XML to JSON | `cargo install xq` |

### CSV & Tabular Data

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **miller** `mlr` | Swiss-army knife for CSV/TSV/JSON | `mlr --csv filter '$revenue > 1000' then sort-by revenue data.csv` — operates on named fields, not column positions | `brew install miller` |
| ② | **xsv** / **qsv** | Fast CSV toolkit | Index, slice, search, join, stats on CSVs at Rust speed; `qsv` is the maintained fork | `cargo install qsv` |
| ③ | **csvkit** | CSV suite (`csvsql`, `csvstat`) | `csvsql --query "SELECT * FROM data WHERE age > 30"` — SQL on CSV files; `csvstat` for structured stats | `pip install csvkit` |

---

## Networking & APIs

### HTTP & APIs

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **httpie** `http` | Human-friendly HTTP client | `http --print=b GET api.example.com/data` — auto-formatted JSON; cleaner syntax than curl for API scripting | `brew install httpie` |
| ② | **xh** | Fast Rust-based HTTP client | HTTPie-compatible syntax, 10× faster; `--offline` previews requests; best for high-throughput API scripting | `cargo install xh` |
| ③ | **curlie** | curl with httpie formatting | Full curl power + readable output; drop-in for scripts needing curl semantics with human-readable defaults | `brew install curlie` |

### DNS & Network Diagnostics

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **dog** | `dig` replacement | `dog example.com A --json` — structured JSON DNS responses; DoH/DoT support | `brew install dog` |
| ② | **nmap** | Network scanner | The definitive port scanner; `-oJ scan.json` for structured output; scriptable NSE scripts | `brew install nmap` |
| ③ | **bandwhich** | Bandwidth by process | Shows exactly which process is consuming bandwidth; `--raw` for machine-readable output | `cargo install bandwhich` |

### HTML & Web Scraping

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **pup** | HTML parser (jq for HTML) | `curl -s url \| pup 'table td json{}'` — CSS selectors → structured JSON; composable with jq | `brew install pup` |
| ② | **htmlq** | CSS selector extractor | `curl -s url \| htmlq --text 'h1'` — Rust-fast, zero dependencies, single binary | `cargo install htmlq` |
| ③ | **playwright** CLI | Headless browser automation | `playwright screenshot --browser=chromium url out.png`; handles JS-rendered SPAs that curl cannot | `pip install playwright && playwright install chromium` |

---

## Developer Tools

### Git & Version Control

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **gh** | Official GitHub CLI | PRs, issues, Actions, releases — `gh pr list --json number,title,state \| jq` | `brew install gh` |
| ② | **delta** | Syntax-highlighting diff pager | Drop-in `GIT_PAGER`; side-by-side diffs with syntax color; `--diff-highlight` | `cargo install git-delta` |
| ③ | **git-extras** | 60+ extra git subcommands | `git summary`, `git undo`, `git pr`, `git rename-branch` — fills gaps in core git | `brew install git-extras` |

### Database & SQL

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **duckdb** | In-process analytical SQL | `duckdb -c "SELECT * FROM 'data.parquet'"` — zero-setup SQL on CSV/Parquet/JSON/Arrow; blazing fast | `brew install duckdb` |
| ② | **usql** | Universal SQL CLI | One interface for PostgreSQL, MySQL, SQLite, BigQuery, Snowflake, 40+ backends | `brew install xo/xo/usql` |
| ③ | **sqlite3** | SQLite CLI | `.mode json` + `.output` make it a reliable pipeline component; ships on every OS | `apt install sqlite3` |

### Testing & Benchmarking

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **hyperfine** | Command benchmarking | `--export-json results.json`; statistical analysis with warmup and pairwise comparison | `cargo install hyperfine` |
| ② | **k6** | HTTP load testing | `k6 run --out json=results.ndjson script.js` — JS-scripted, CI-native, structured metrics output | `brew install k6` |
| ③ | **vegeta** | HTTP load testing pipeline | `echo "GET http://host/" \| vegeta attack -rate=100 \| vegeta report --type=json` — pure Unix pipe design | `brew install vegeta` |

---

## Documents & Media

### Document Conversion

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **pandoc** | Universal document converter | Markdown → DOCX/PDF/EPUB/HTML/LaTeX and 40+ more; Lua filters for programmatic transforms | `brew install pandoc` |
| ② | **weasyprint** | HTML/CSS → PDF | Full CSS Paged Media support; far better fidelity than wkhtmltopdf; fully headless | `pip install weasyprint` |
| ③ | **asciidoctor** | AsciiDoc → HTML/PDF/EPUB | Publication-quality PDFs; used by O'Reilly; `asciidoctor-pdf` in one command | `gem install asciidoctor asciidoctor-pdf` |

### PDF Tools

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **qpdf** | PDF transformer | `qpdf --json input.pdf` — full JSON representation of PDF internals; split/merge/decrypt/repair | `brew install qpdf` |
| ② | **poppler-utils** | PDF extraction suite | `pdftotext -layout file.pdf -` pipes PDF text to stdout; `pdfinfo` for metadata; ubiquitous | `brew install poppler` |
| ③ | **pdfcpu** | PDF processor | `pdfcpu validate -mode strict` and `pdfcpu info --json` — single static binary, no dependencies, CI-friendly | `brew install pdfcpu` |

### Image Processing

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **libvips** `vipsthumbnail` | High-performance image processing | Streaming/tiling processes gigapixel images in constant memory; far faster than ImageMagick for batch ops | `brew install vips` |
| ② | **imagemagick** `magick` | Image conversion & manipulation | `magick mogrify -resize 50% -format webp *.jpg` batch-converts directories; 200+ formats | `brew install imagemagick` |
| ③ | **exiftool** | Image/media metadata RW | `exiftool -json *.jpg` — rich structured JSON metadata; handles 150+ file formats including video | `brew install exiftool` |

### Audio & Video

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **ffmpeg** | Universal media processor | `ffprobe -v quiet -print_format json -show_streams` for structured metadata; `-nostdin` makes it pipeline-safe | `brew install ffmpeg` |
| ② | **sox** | Audio processing & analysis | `sox --stat` and `sox --info` give machine-readable audio stats; clean DSP pipeline syntax | `brew install sox` |
| ③ | **mediainfo** | Media file inspector | `mediainfo --Output=JSON` — rich structured JSON covering every codec parameter, bitrate, and track | `brew install mediainfo` |

---

## Security

### Encryption & Secrets

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **age** | Modern file encryption | `age -r pubkey file > file.age` — dead simple, no keyring, no GPG complexity; designed for scripting | `brew install age` |
| ② | **sops** | Secrets manager for YAML/JSON/ENV | Encrypts only values (keys stay readable); AWS KMS/GCP/age/PGP backends; `sops --decrypt` in pipelines | `brew install sops` |
| ③ | **pass** | Unix password manager | GPG-encrypted files in git; `pass show token \| head -1` pipes secrets cleanly; auditable | `brew install pass` |

### Vulnerability Scanning

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **trivy** | Container & filesystem CVE scanner | `trivy image --format json myapp:latest` — zero-config, structured JSON CVE reports; CI/CD standard | `brew install trivy` |
| ② | **nuclei** | Template-based web vulnerability scanner | `nuclei -target example.com -json-export results.json` — thousands of checks, structured output | `brew install nuclei` |
| ③ | **trufflehog** | Secret/credential leak scanner | Scans git history for real secrets (not just patterns); `--json` structured findings | `brew install trufflesecurity/trufflehog/trufflehog` |

---

## Infrastructure & Cloud

### Cloud & IaC

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **terraform** / **tofu** | Infrastructure as code | `terraform plan -json` and `apply -auto-approve` run fully non-interactively; JSON output for pipeline consumption | `brew install opentofu` |
| ② | **rclone** | Cloud storage sync (70+ providers) | `rclone lsjson remote:bucket` and `rclone copy --json` — the rsync for S3/GCS/Azure/Dropbox | `brew install rclone` |
| ③ | **ansible** | Agentless server automation | Push config to many servers via SSH with YAML playbooks; `--check` dry-run mode | `pip install ansible` |

### Kubernetes & Containers

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **kubectl** | Kubernetes control CLI | `kubectl get pods -o json \| jq` and `kubectl wait --for=condition=ready` make it pipeline-composable | `brew install kubectl` |
| ② | **stern** | Multi-pod log tailer | `stern --output json deployment/myapp` emits NDJSON from all matching pods — far better than `kubectl logs` | `brew install stern` |
| ③ | **crane** | Container registry CLI (no Docker daemon) | `crane digest image:tag` and `crane copy src dst` — registry ops without Docker overhead | `brew install crane` |

### Monitoring & Observability

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **vector** | Log/metric/event pipeline | Config-driven collection → transform → route; 50+ sources/sinks; `--once` for one-shot runs | `brew install vector` |
| ② | **lnav** | Log file analyzer | Auto-detects log format; SQL queries over log files; non-interactive `--headless` mode | `brew install lnav` |
| ③ | **logcli** | Grafana Loki CLI | `logcli query '{app="myapp"}' --output=jsonl --limit=1000` — streams Loki logs into pipelines | Download from Grafana GitHub |

---

## Messaging & Communication

### Message Queues

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **kcat** (kafkacat) | Kafka producer/consumer | `kcat -C -b broker -t topic -e -J` consumes a full topic as NDJSON — standard Kafka debug/pipeline tool | `brew install kcat` |
| ② | **nats** CLI | NATS messaging operations | `nats pub subject "payload"` and `nats stream report --json` — structured ops against NATS JetStream | `brew install nats-io/nats-tools/nats` |
| ③ | **ntfy** | Push notifications from shell | `curl -d "Build done" ntfy.sh/mytopic` — one line, any device; priorities and attachments supported | `brew install ntfy` |

### Email

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **msmtp** | SMTP client for scripts | `echo "Subject: Alert\n\nBody" \| msmtp recipient@example.com` — minimal, TLS/OAuth2, no daemon | `brew install msmtp` |
| ② | **himalaya** | Email CLI (IMAP/SMTP) | `himalaya list --output=json` and `himalaya read <id> --output=json` — inbox as structured JSON for automation | `cargo install himalaya` |
| ③ | **swaks** | SMTP testing tool | `swaks --to user@host --server smtp --auth --tls` — full TLS/auth test emails; detailed SMTP conversation output | `brew install swaks` |

---

## Package Management

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **nix** | Reproducible package manager | `nix-shell -p tool1 tool2 --run "..."` — hermetic environments; bit-for-bit reproducible builds | `curl -L https://nixos.org/nix/install \| sh` |
| ② | **uv** | Ultra-fast Python package manager | `uv run script.py` installs deps and runs in isolation in one command; 10-100× faster than pip | `brew install uv` |
| ③ | **brew** | macOS/Linux package manager | `brew bundle --file=Brewfile` installs a declared set of packages non-interactively from version control | `brew install homebrew/brew` |

---

## Compression & Archive

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **zstd** | Zstandard compressor | Best ratio:speed balance; `-T0` uses all cores; `--no-progress` pipeline-safe; default in many Linux kernels | `brew install zstd` |
| ② | **pixz** | Parallel indexed XZ | Creates indexed `.tar.xz` allowing random file access without full decompression — critical for large archives | `brew install pixz` |
| ③ | **brotli** | Brotli compressor | `brotli -c file > file.br` — better web compression than gzip; widely supported; single-binary | `brew install brotli` |

---

## Niche & Industry Domains

### Bioinformatics

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **samtools** | SAM/BAM/CRAM alignment file operations | The de facto standard in genomics pipelines; multi-threaded, terabyte-scale; composable subcommands | `conda install -c bioconda samtools` |
| ② | **seqkit** | FASTA/FASTQ sequence manipulation | `seqkit stats --tabular` — machine-readable stats; Go-fast; filter/split/translate/search with clean output | `conda install -c bioconda seqkit` |
| ③ | **bcftools** | VCF/BCF variant file operations | `bcftools view \| bcftools filter \| bcftools stats` — perfectly composable pipeline for genomic variants | `conda install -c bioconda bcftools` |

### Geospatial / GIS

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **gdal** `ogr2ogr` | Geospatial format converter | 200+ formats; `ogr2ogr -f GeoJSON` in a shell script replaces entire GUI workflows; `gdalinfo --json` | `brew install gdal` |
| ② | **tippecanoe** | Vector tile generator | Takes millions of GeoJSON features → optimally simplified `.mbtiles` in one command; nothing else scales like it | `brew install tippecanoe` |
| ③ | **rio** (rasterio) | Raster file CLI | `rio info --indent 2` returns clean JSON raster metadata; `rio calc` runs band math in pipelines | `pip install rasterio` |

### Finance & Accounting

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **hledger** | Plain-text double-entry accounting | `hledger balance --output-format=json` — fully machine-readable financial data; version-control friendly | `brew install hledger` |
| ② | **ledger** | Original plain-text accounting CLI | Powerful expression language; CSV output; beloved for keeping finances in git as plain text | `brew install ledger` |
| ③ | **piecash** | GnuCash Python/CLI bridge | Extract GnuCash SQLite/XML data into CSV/DataFrames for batch analysis — bridges GUI accounting to scripting | `pip install piecash` |

### Machine Learning & AI

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **ollama** | Local LLM inference | `ollama run llama3 "prompt"` non-interactively; serves OpenAI-compatible REST API — agent pipelines without cloud | `curl -fsSL https://ollama.com/install.sh \| sh` |
| ② | **huggingface-cli** | Hugging Face Hub manager | `huggingface-cli download org/model --local-dir ./model` — resume-capable model downloads for CI/CD ML pipelines | `pip install huggingface_hub` |
| ③ | **mlflow** CLI | ML experiment tracking & serving | `mlflow run .` executes training non-interactively; `mlflow models serve` deploys REST endpoint | `pip install mlflow` |

### Chemistry & Cheminformatics

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **openbabel** | Chemical format converter | `obabel input.sdf -O output.smiles` — 100+ formats; `--gen3d` generates 3D coordinates; the ffmpeg of cheminformatics | `brew install open-babel` |
| ② | **mafft** | Multiple sequence aligner | `mafft --auto --thread 8 input.fasta > aligned.fasta` — fully non-interactive; handles thousands of sequences | `brew install mafft` |
| ③ | **rdkit** | Cheminformatics toolkit | Industry standard for molecular fingerprinting and similarity; scriptable for batch property calculation at scale | `conda install -c conda-forge rdkit` |

### Astronomy

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **stilts** | Astronomical table operations | `stilts tpipe in=catalog.fits cmd='select "Vmag<15"' out=bright.csv` — the jq/awk of astronomy catalogs | `java -jar stilts.jar` (download from Starlink) |
| ② | **SExtractor** | Astronomical source detection | Detects stars/galaxies in FITS images from a config file; outputs ASCII/FITS catalogs; standard in telescope pipelines | `apt install sextractor` |
| ③ | **astropy** CLI | FITS file utilities | `python -m astropy.io.fits info file.fits` — foundation of Python astronomy pipelines; inspect any FITS file | `pip install astropy` |

### Typography & Fonts

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **fonttools** `pyftsubset` | Font manipulation suite | `pyftsubset font.ttf --unicodes="U+0020-007E" --flavor=woff2` — optimized web font subsets in one command | `pip install fonttools` |
| ② | **woff2** | WOFF2 compress/decompress | `woff2_compress font.ttf` — single-purpose, does one thing perfectly; standard in web font build pipelines | `brew install woff2` |
| ③ | **harfbuzz** `hb-shape` | Text shaping inspector | `hb-shape font.ttf --text="Hello"` outputs exact glyph IDs and advances — essential for font rendering debug | `brew install harfbuzz` |

### Blockchain & Crypto

| # | Tool | What it does | Killer feature | Install |
|---|------|-------------|----------------|---------|
| ① | **cast** (Foundry) | Ethereum EVM interaction | `cast call contract "balanceOf(address)(uint256)" addr --rpc-url $RPC` — reads chain state non-interactively; `cast abi-decode` for calldata | `curl -L https://foundry.paradigm.xyz \| bash && foundryup` |
| ② | **bitcoin-cli** | Bitcoin Core RPC interface | `bitcoin-cli getblockchaininfo` / `getrawtransaction <txid> true` — structured JSON; the standard Bitcoin node interface | Ships with Bitcoin Core |
| ③ | **web3** / **seth** | Ethereum Unix-style CLI | `seth call contract sig args` — pure Unix pipeline design for blockchain data; plain text output for further processing | `nix-env -iA dapptools.seth` |

---

## Install Method Reference

| Method | Description | Bootstrap |
|--------|-------------|-----------|
| `brew` | Homebrew — macOS & Linux | `curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh \| bash` |
| `cargo` | Rust package manager | `curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs \| sh` |
| `pip` / `pipx` | Python — prefer `pipx` for CLI tools | `brew install pipx` |
| `go install` | Go tools | `brew install go` |
| `conda` | Scientific Python/bioinformatics | `brew install miniforge` |
| `apt` | Debian/Ubuntu system packages | Built-in |
| `nix` | Reproducible, any platform | `curl -L https://nixos.org/nix/install \| sh` |

---

## Sources

- [modern-unix](https://github.com/ibraheemdev/modern-unix) — 32k+ stars
- [awesome-cli-apps](https://github.com/agarrharr/awesome-cli-apps) — 19k+ stars
- [awesome-shell](https://github.com/alebcay/awesome-shell) — 36k+ stars
- [terminaltrove.com](https://terminaltrove.com) — curated TUI/CLI directory
