#!/usr/bin/env python3
"""
Forcefield Scrapling Worker

Communicates via newline-delimited JSON (NDJSON) over stdin/stdout.
Strictly zero network requests: uses scrapling.Adaptor for HTML parsing only.
No Fetcher, no network sockets, no external resource loading, no JavaScript.
"""

from __future__ import annotations

import json
import sys
import traceback
from typing import Any

try:
    from scrapling import Adaptor
except ImportError as e:
    _IMPORT_ERROR = str(e)
    Adaptor = None  # type: ignore[assignment,misc]
else:
    _IMPORT_ERROR = None

VERSION = "1.0"
INPUT_SIZE_LIMIT = 5 * 1024 * 1024   # 5 MiB
OUTPUT_SIZE_LIMIT = 2 * 1024 * 1024  # 2 MiB

KNOWN_TARGETS = {"article", "headings", "links", "tables", "metadata"}


def _write(obj: dict[str, Any]) -> None:
    """Write a single NDJSON line to stdout and flush."""
    line = json.dumps(obj, ensure_ascii=False)
    sys.stdout.write(line + "\n")
    sys.stdout.flush()


def _error_response(req_id: str, message: str) -> dict[str, Any]:
    return {
        "version": VERSION,
        "id": req_id,
        "success": False,
        "error": message,
    }


def _extract_headings(page: Any) -> list[dict[str, Any]]:
    headings = []
    for level in range(1, 7):
        tag = f"h{level}"
        elements = page.css(tag)
        if elements:
            for el in elements:
                text = el.text.strip() if hasattr(el, "text") else ""
                if text:
                    headings.append({"level": level, "text": text})
    # Sort by document order is not guaranteed here; return as found per level.
    return headings


def _extract_links(page: Any) -> list[dict[str, Any]]:
    links = []
    elements = page.css("a[href]")
    if elements:
        for el in elements:
            href = el.attrib.get("href", "") if hasattr(el, "attrib") else ""
            text = el.text.strip() if hasattr(el, "text") else ""
            if href:
                links.append({"text": text, "url": href})
    return links


def _extract_tables(page: Any) -> list[dict[str, Any]]:
    tables = []
    table_elements = page.css("table")
    if not table_elements:
        return tables
    for tbl in table_elements:
        headers: list[str] = []
        rows: list[list[str]] = []
        # Extract headers from <thead> <th> or first <tr> <th>
        thead_ths = tbl.css("thead th")
        if thead_ths:
            headers = [th.text.strip() for th in thead_ths]
        else:
            # First row th elements as headers
            first_row_ths = tbl.css("tr:first-child th")
            if first_row_ths:
                headers = [th.text.strip() for th in first_row_ths]
        # Extract body rows
        body_rows = tbl.css("tbody tr")
        if not body_rows:
            # Fallback: all trs, skip header row
            all_rows = tbl.css("tr")
            if all_rows:
                start = 1 if headers else 0
                for tr in list(all_rows)[start:]:
                    cells = tr.css("td")
                    if cells:
                        rows.append([td.text.strip() for td in cells])
        else:
            for tr in body_rows:
                cells = tr.css("td")
                if cells:
                    rows.append([td.text.strip() for td in cells])
        tables.append({"headers": headers, "rows": rows})
    return tables


def _extract_metadata(page: Any) -> dict[str, str]:
    meta: dict[str, str] = {}
    # Collect standard meta tags
    for m in page.css("meta[name]"):
        name = m.attrib.get("name", "").lower()
        content = m.attrib.get("content", "")
        if name and content:
            meta[name] = content
    # Open Graph / Twitter card
    for m in page.css("meta[property]"):
        prop = m.attrib.get("property", "").lower()
        content = m.attrib.get("content", "")
        if prop and content:
            meta[prop] = content
    for m in page.css("meta[name^='twitter:']"):
        name = m.attrib.get("name", "").lower()
        content = m.attrib.get("content", "")
        if name and content:
            meta[name] = content
    # Canonical link
    canonical = page.css("link[rel='canonical']")
    if canonical:
        href = canonical[0].attrib.get("href", "")
        if href:
            meta["canonical"] = href
    return meta


def _extract_article(page: Any) -> str:
    """
    Extract main article text. Prefer <article>, <main>, then <body>.
    Returns plain text (no HTML).
    """
    for selector in ("article", "main", "[role='main']", "body"):
        elements = page.css(selector)
        if elements:
            el = elements[0]
            text = el.text.strip() if hasattr(el, "text") else ""
            if text:
                return text
    # Fallback: get_all_text on page
    try:
        return page.get_all_text(ignore_tags=("script", "style")).strip()
    except Exception:
        return ""


def _get_title(page: Any) -> str:
    title_els = page.css("title")
    if title_els:
        return title_els[0].text.strip()
    h1_els = page.css("h1")
    if h1_els:
        return h1_els[0].text.strip()
    return ""


def handle_extract(request: dict[str, Any]) -> dict[str, Any]:
    req_id = request.get("id", "")
    html_content = request.get("html", "")
    url = request.get("url", "")
    targets = set(request.get("targets", list(KNOWN_TARGETS)))

    if not html_content:
        return _error_response(req_id, "html field is required and must not be empty")

    try:
        # Adaptor performs purely local HTML parsing; no network activity.
        page = Adaptor(html_content, url=url if url else None)
    except Exception as exc:
        return _error_response(req_id, f"scrapling.Adaptor failed: {exc}")

    data: dict[str, Any] = {}

    try:
        data["title"] = _get_title(page)
    except Exception as exc:
        data["title"] = ""

    if "article" in targets:
        try:
            data["article"] = _extract_article(page)
        except Exception as exc:
            data["article"] = ""

    if "headings" in targets:
        try:
            data["headings"] = _extract_headings(page)
        except Exception as exc:
            data["headings"] = []

    if "links" in targets:
        try:
            data["links"] = _extract_links(page)
        except Exception as exc:
            data["links"] = []

    if "tables" in targets:
        try:
            data["tables"] = _extract_tables(page)
        except Exception as exc:
            data["tables"] = []

    if "metadata" in targets:
        try:
            data["metadata"] = _extract_metadata(page)
        except Exception as exc:
            data["metadata"] = {}

    return {
        "version": VERSION,
        "id": req_id,
        "success": True,
        "data": data,
    }


def process_request(raw: str) -> dict[str, Any]:
    try:
        request = json.loads(raw)
    except json.JSONDecodeError as exc:
        return _error_response("", f"invalid JSON: {exc}")

    req_id = request.get("id", "")
    action = request.get("action", "")

    req_version = request.get("version", "")
    if req_version != VERSION:
        return _error_response(req_id, f"unsupported version: {req_version!r}")

    if action == "extract":
        if Adaptor is None:
            return _error_response(
                req_id,
                f"scrapling is not installed: {_IMPORT_ERROR}. "
                "Install it with: pip install scrapling",
            )
        return handle_extract(request)
    else:
        return _error_response(req_id, f"unknown action: {action!r}")


def main() -> None:
    # Use binary stdin to avoid codec issues; decode manually.
    stdin_bin = sys.stdin.buffer

    while True:
        try:
            line_bytes = stdin_bin.readline()
        except (OSError, IOError):
            break

        if not line_bytes:
            # EOF — graceful shutdown.
            break

        # Enforce input size limit before decoding.
        if len(line_bytes) > INPUT_SIZE_LIMIT:
            response = _error_response("", "input payload exceeds 5 MiB limit")
            _write(response)
            continue

        try:
            raw = line_bytes.decode("utf-8", errors="replace").rstrip("\n\r")
        except Exception:
            _write(_error_response("", "failed to decode input line as UTF-8"))
            continue

        if not raw.strip():
            continue

        try:
            response = process_request(raw)
        except Exception as exc:
            tb = traceback.format_exc()
            response = _error_response("", f"internal worker error: {exc}\n{tb}")

        # Enforce output size limit.
        try:
            serialized = json.dumps(response, ensure_ascii=False)
        except Exception as exc:
            serialized = json.dumps(_error_response("", f"JSON serialization error: {exc}"))

        if len(serialized.encode("utf-8")) > OUTPUT_SIZE_LIMIT:
            response = _error_response(
                response.get("id", ""),
                "extraction output exceeds 2 MiB limit; reduce targets or truncate HTML",
            )
            serialized = json.dumps(response, ensure_ascii=False)

        sys.stdout.write(serialized + "\n")
        sys.stdout.flush()


if __name__ == "__main__":
    main()
