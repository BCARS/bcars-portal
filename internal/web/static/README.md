# Vendored front-end assets

These files are compiled into the `portal` binary (`internal/web/static.go`) and
served from `/static/`. Nothing here is fetched at runtime: the admin UI must
render on a host with no outbound network access, and a page that handles member
PII should not take a CDN's supply chain as its own (bcars-portal-chp).

Asset file names carry the version they contain. Upgrading means adding the new
file, updating the constant in `internal/web/static.go` and the `<script>` tag in
`internal/web/templates/layout.html`, and deleting the old file — the URL changes
with the bytes, so responses are cached indefinitely.

| File | Upstream | Version | License | SHA-256 |
| --- | --- | --- | --- | --- |
| `htmx-2.0.4.min.js` | https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js | 2.0.4 | 0BSD (`LICENSE-htmx.txt`) | `e209dda5c8235479f3166defc7750e1dbcd5a5c1808b7792fc2e6733768fb447` |

To verify a vendored file against its recorded digest:

```bash
sha256sum internal/web/static/htmx-2.0.4.min.js
```

`TestVendoredAssetDigests` in `internal/web/static_test.go` runs that check as
part of `make test`, so a modified or truncated asset fails the gate rather than
the browser.
