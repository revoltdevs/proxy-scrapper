# proxy-scraper

A fast proxy aggregation and validation tool for collecting working HTTP proxies from public sources.

## Overview

`proxy-scraper` is a lightweight Go utility that fetches, deduplicates, and validates HTTP proxies from multiple public sources. It performs real connectivity tests to ensure proxies are functional before writing them to an output file.

## Features

- Aggregates proxies from multiple raw text and API endpoints
- Extracts multiple proxies per line from source data
- Deduplicates proxy lists before validation to reduce redundant checks
- Flexible validation modes: HTTP GET, CONNECT, or both
- Configurable worker pools with granular timeout controls
- Writes validated proxies to a single output file
- Simple single-binary deployment with no external dependencies
- Customizable via command-line flags
- Optional custom sources file support

## How It Works

1. Fetches proxy lists from preconfigured public sources (or custom sources file)
2. Parses and extracts all proxies from each line using regex
3. Deduplicates entries across all sources
4. Tests each unique proxy using the specified validation mode
5. Applies multiple timeout layers (dial, read/write, HTTP fetch, total runtime) to filter non-responsive proxies
6. Writes working proxies to the output file in `IP:PORT` format

## Validation Modes

- **http**: Validates proxies using HTTP GET requests through the proxy
- **connect**: Validates proxies using HTTP CONNECT method (port 443)
- **both**: Accepts proxies that pass either HTTP or CONNECT validation (default)

## Usage

Build the binary:

```bash
go build -o proxy-scraper
```

Run the tool with default settings:

```bash
./proxy-scraper
```

Run with custom configuration:

```bash
./proxy-scraper -workers 500 -mode connect -out validated.txt -dial-timeout 3s -max 1000
```

Use a custom sources file:

```bash
./proxy-scraper -sources my-sources.txt -max 1000
```

## Command-Line Flags

| Flag | Description | Default |
|------|-------------|---------|
| `-out` | Output file path for validated proxies | `proxies.txt` |
| `-sources` | Optional path to custom sources file (one URL per line, format: `name=URL` or just `URL`) | (uses built-in sources) |
| `-mode` | Validation mode: `http`, `connect`, or `both` | `both` |
| `-workers` | Number of concurrent validation workers | `300` |
| `-fetchers` | Maximum concurrent source fetches | `20` |
| `-max` | Stop after N valid proxies (0 = no limit) | `0` |
| `-total-timeout` | Total runtime timeout for entire operation | `2m` |
| `-http-timeout` | HTTP fetch timeout for downloading source lists | `20s` |
| `-dial-timeout` | TCP dial timeout for proxy validation | `4s` |
| `-rw-timeout` | Read/write timeout for proxy communication | `4s` |
| `-test-host` | Host used for validation tests (GET and CONNECT) | `example.com` |
| `-ua` | User-Agent header for fetching source lists | `proxy-scraper/1.0 (+github)` |

## Output Format

Each line in the output file contains a single proxy in the following format:

```
192.168.1.100:8080
10.0.0.50:3128
203.0.113.42:80
```

The output is sorted alphabetically for consistency.

## Example Output

<img width="172" height="70" alt="image" src="https://github.com/user-attachments/assets/4c2666ae-c94b-43e0-85f5-d492907c834c" />

## Custom Sources File

You can provide your own sources file with the `-sources` flag. Format:

```
# Comments start with #
https://example.com/proxies.txt
MySource=https://example.com/api/proxies
https://another-source.com/list.txt
```

Lines can be:
- Plain URLs
- `name=URL` format for labeled sources
- Comments (lines starting with `#`)

## Requirements

- Go 1.20 or higher
- Internet connectivity to reach public proxy sources

## Design Philosophy

This tool prioritizes speed and simplicity. It performs no rate limiting, includes no progress indicators, and produces minimal output. The focus is on efficiently collecting functional proxies for infrastructure testing, network tooling, and data collection workflows.

Validation is strict: proxies must respond within the configured timeout layers (TCP dial, read/write operations, HTTP fetches, and total execution time). This ensures the output file contains only immediately usable proxies.
