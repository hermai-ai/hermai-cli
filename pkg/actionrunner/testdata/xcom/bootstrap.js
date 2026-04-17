// xcom_bootstrap.js
//
// Session-bootstrap step for x.com's `x-client-transaction-id` signer.
//
// Port of pkg/sessions/xcom/bootstrap.go to pure JavaScript, executed in
// the goja sandbox (hermai/pkg/signer.JSBootstrap). The sandbox exposes
// only:
//
//   hermai.fetch(url, opts)            -> {status, url, headers, body}
//   hermai.selectAll(html, selector)   -> [{tag, attrs, text, children}]
//   hermai.sha256(str), hermai.hmacSha256(key, msg)
//   hermai.base64Encode(str), hermai.base64EncodeURL(str),
//   hermai.base64Decode(str), hermai.hex(str), hermai.hexDecode(str),
//   hermai.randomHex(n)
//
// Standard JS is available (Math, JSON, RegExp, String, Array). NO DOM,
// no `fetch`, no `crypto`, no `TextEncoder`, no `atob`/`btoa`.
//
// The function returns strings for every field — the host does its own
// coercion, but we spell it out here to keep the contract obvious.
//
// Reference:
//   pkg/sessions/xcom/bootstrap.go   (Go impl being ported)
//   pkg/sessions/xcom/bootstrap_test.go   (pinned test vectors)
//   https://github.com/iSarabjitDhiman/XClientTransaction   (upstream)

// --------------------------------------------------------------------------
// Constants
// --------------------------------------------------------------------------

var HOME_URL = "https://x.com";
var ONDEMAND_URL_TEMPLATE = "https://abs.twimg.com/responsive-web/client-web/ondemand.s.%sa.js";
var DEFAULT_USER_AGENT =
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
    "AppleWebKit/537.36 (KHTML, like Gecko) Chrome/127.0.0.0 Safari/537.36";

var B64_ALPHABET =
    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

// Regexes. Mirror bootstrap.go verbatim. JS has no (?i) global flag
// so we use the `i` flag on the meta regex. The `g` flag is used for
// multi-match scans.
var META_KEY_RE = /<meta\s+name=["']twitter-site-verification["']\s+content=["']([^"']+)["']/i;
var MIGRATION_RE =
    /(http(?:s)?:\/\/(?:www\.)?(?:twitter|x){1}\.com(?:\/x)?\/migrate(?:[\/?])?tok=[a-zA-Z0-9%\-_]+)+/;
var ONDEMAND_INDEX_RE = /,(\d+):["']ondemand\.s["']/;
// ondemandIndicesRE is applied with `g` to collect all matches. The Go
// inner capture is \d{1,2} (the int in w[NN]); we preserve that.
var ONDEMAND_INDICES_RE = /\(\w{1}\[(\d{1,2})\],\s*16\)/g;
var NON_DIGIT_RE = /[^\d]+/g;

// --------------------------------------------------------------------------
// Helpers: base64 decode to byte-number array
// --------------------------------------------------------------------------

// base64ToBytes: accepts either standard (+/) or URL-safe (-_) alphabets,
// ignores "=" padding and whitespace. Returns an array of ints in [0,255].
// Mirrors the helper in schemas/xcom/signer.js for parity.
function base64ToBytes(s) {
    if (typeof s !== "string") {
        throw new Error("base64ToBytes: expected string, got " + typeof s);
    }

    var lookup = {};
    for (var k = 0; k < B64_ALPHABET.length; k++) {
        lookup[B64_ALPHABET.charAt(k)] = k;
    }
    lookup["-"] = lookup["+"];
    lookup["_"] = lookup["/"];

    var chars = [];
    for (var i = 0; i < s.length; i++) {
        var c = s.charAt(i);
        if (c === "=" || c === " " || c === "\n" || c === "\r" || c === "\t") {
            continue;
        }
        if (!(c in lookup)) {
            throw new Error("base64ToBytes: invalid char '" + c + "'");
        }
        chars.push(lookup[c]);
    }

    var out = [];
    var j = 0;
    while (j + 4 <= chars.length) {
        var n = (chars[j] << 18) |
                (chars[j + 1] << 12) |
                (chars[j + 2] << 6) |
                 chars[j + 3];
        out.push((n >> 16) & 0xFF);
        out.push((n >> 8) & 0xFF);
        out.push(n & 0xFF);
        j += 4;
    }

    var tail = chars.length - j;
    if (tail === 2) {
        var m2 = (chars[j] << 18) | (chars[j + 1] << 12);
        out.push((m2 >> 16) & 0xFF);
    } else if (tail === 3) {
        var m3 = (chars[j] << 18) |
                 (chars[j + 1] << 12) |
                 (chars[j + 2] << 6);
        out.push((m3 >> 16) & 0xFF);
        out.push((m3 >> 8) & 0xFF);
    } else if (tail === 1) {
        throw new Error("base64ToBytes: invalid 1-char tail");
    }
    return out;
}

// --------------------------------------------------------------------------
// HTTP
// --------------------------------------------------------------------------

function browserHeaders() {
    return {
        "User-Agent": DEFAULT_USER_AGENT,
        "Accept-Language": "en-US,en;q=0.9",
        "Referer": "https://x.com",
        "X-Twitter-Active-User": "yes",
        "X-Twitter-Client-Language": "en"
    };
}

function httpGet(url) {
    var resp = hermai.fetch(url, {headers: browserHeaders()});
    if (!resp || typeof resp !== "object") {
        throw new Error("fetch: empty response for " + url);
    }
    var status = resp.status | 0;
    if (status < 200 || status >= 300) {
        throw new Error(url + ": unexpected status " + status);
    }
    return resp;
}

// httpPostForm posts an application/x-www-form-urlencoded body built from
// the given flat {name: value} map. Used on the migration <form> path.
function httpPostForm(url, fields) {
    var body = "";
    var first = true;
    for (var name in fields) {
        if (!Object.prototype.hasOwnProperty.call(fields, name)) {
            continue;
        }
        if (name === "") {
            continue;
        }
        if (!first) {
            body += "&";
        }
        first = false;
        body += encodeURIComponent(name) + "=" + encodeURIComponent(fields[name]);
    }
    var headers = browserHeaders();
    headers["Content-Type"] = "application/x-www-form-urlencoded";
    var resp = hermai.fetch(url, {
        method: "POST",
        headers: headers,
        body: body
    });
    if (!resp || typeof resp !== "object") {
        throw new Error("fetch: empty response for " + url);
    }
    var status = resp.status | 0;
    if (status < 200 || status >= 300) {
        throw new Error(url + ": unexpected status " + status);
    }
    return resp;
}

// --------------------------------------------------------------------------
// Migration handling
// --------------------------------------------------------------------------

// maybeFollowMigration inspects the home body for x.com's pre-auth
// migration redirect. Returns either the original body string or the body
// of the followed-migration response. Mirrors the Go implementation.
function maybeFollowMigration(body) {
    var hasMigrationHint =
        MIGRATION_RE.test(body) ||
        body.indexOf('name="f"') >= 0 ||
        body.indexOf('http-equiv="refresh"') >= 0;
    if (!hasMigrationHint) {
        return body;
    }

    // Prefer the <form name="f"> path.
    var forms = hermai.selectAll(body, 'form[name="f"]');
    if (forms && forms.length > 0) {
        var form = forms[0];
        var action = attrValue(form, "action");
        if (!action) {
            throw new Error("migration <form> missing action");
        }
        var inputs = hermai.selectAll(body, 'form[name="f"] input');
        var values = {};
        if (inputs) {
            for (var i = 0; i < inputs.length; i++) {
                var inp = inputs[i];
                var name = attrValue(inp, "name");
                var val = attrValue(inp, "value");
                if (name) {
                    values[name] = val || "";
                }
            }
        }
        var postResp = httpPostForm(action, values);
        return postResp.body || "";
    }

    // Meta-refresh path: <meta http-equiv="refresh" content="0; url=...">
    var metas = hermai.selectAll(body, 'meta[http-equiv="refresh"]');
    if (metas && metas.length > 0) {
        var meta = metas[0];
        var content = attrValue(meta, "content") || "";
        var m = content.match(MIGRATION_RE);
        if (m && m[0]) {
            var resp = httpGet(m[0]);
            return resp.body || "";
        }
    }

    // Final fallback: GET the URL the raw-body regex caught, if any.
    var mm = body.match(MIGRATION_RE);
    if (mm && mm[0]) {
        var r2 = httpGet(mm[0]);
        return r2.body || "";
    }

    return body;
}

// --------------------------------------------------------------------------
// DOM helpers (operate on nodeToJS output: {tag, attrs, text, children})
// --------------------------------------------------------------------------

function attrValue(node, key) {
    if (!node || !node.attrs) {
        return "";
    }
    var v = node.attrs[key];
    if (v === undefined || v === null) {
        return "";
    }
    return String(v);
}

// --------------------------------------------------------------------------
// Home HTML extraction
// --------------------------------------------------------------------------

function extractKey(homeBody) {
    // Prefer regex over raw body — matches the Go behavior (fallback is
    // selectAll, but the regex is deterministic and cheap). The Go code
    // prefers the parsed DOM; we invert priority because the sandbox's
    // selectAll parses the entire page each call, which is expensive on
    // 3 MB HTML. The regex + DOM fallback produces identical results for
    // valid input.
    var m = homeBody.match(META_KEY_RE);
    if (m && m.length >= 2 && m[1]) {
        return m[1];
    }
    var nodes = hermai.selectAll(homeBody, 'meta[name="twitter-site-verification"]');
    if (nodes && nodes.length > 0) {
        var v = attrValue(nodes[0], "content");
        if (v) {
            return v;
        }
    }
    throw new Error("twitter-site-verification meta not found");
}

function extractFrames(homeBody) {
    // CSS selector mirrors Go's `[id^="loading-x-anim"]`.
    var nodes = hermai.selectAll(homeBody, '[id^="loading-x-anim"]');
    if (!nodes || nodes.length === 0) {
        throw new Error("no loading-x-anim elements found");
    }

    // Allocate 4 slots and backfill by id index. Source order for
    // unparseable ids, matching Go.
    var frames = ["", "", "", ""];
    var filled = [false, false, false, false];
    var leftovers = [];

    for (var i = 0; i < nodes.length; i++) {
        var node = nodes[i];
        var id = attrValue(node, "id") || "";
        var d = extractPathD(node);
        var parsed = parseFrameIndex(id);
        if (parsed.ok && parsed.idx >= 0 && parsed.idx < 4) {
            frames[parsed.idx] = d;
            filled[parsed.idx] = true;
        } else {
            leftovers.push(d);
        }
    }

    for (var j = 0; j < frames.length && leftovers.length > 0; j++) {
        if (!filled[j]) {
            frames[j] = leftovers.shift();
            filled[j] = true;
        }
    }

    var out = [];
    for (var k = 0; k < frames.length; k++) {
        if (filled[k]) {
            out.push(frames[k]);
        }
    }
    if (out.length === 0) {
        throw new Error("could not extract any loading-x-anim path d attributes");
    }
    return out;
}

// extractPathD mirrors Go's `frame.children[0].children[1]` semantics,
// operating on the element-only `children` arrays returned by
// hermai.selectAll (nodeToJS flattens text nodes into `text`).
function extractPathD(frame) {
    if (!frame || !frame.children || frame.children.length === 0) {
        return "";
    }
    var firstChild = frame.children[0];
    if (!firstChild || !firstChild.children || firstChild.children.length < 2) {
        return "";
    }
    var nth = firstChild.children[1];
    return attrValue(nth, "d");
}

function parseFrameIndex(id) {
    var prefix = "loading-x-anim-";
    if (id.indexOf(prefix) !== 0) {
        return {ok: false, idx: 0};
    }
    var rest = id.substring(prefix.length);
    if (rest.length === 0) {
        return {ok: false, idx: 0};
    }
    // Reject anything that isn't a non-negative decimal integer; Go's
    // strconv.Atoi returns an error for leading signs / non-digits here
    // because the id pattern never has them, but we match the behavior
    // exactly.
    if (!/^\d+$/.test(rest)) {
        return {ok: false, idx: 0};
    }
    var n = parseInt(rest, 10);
    if (isNaN(n)) {
        return {ok: false, idx: 0};
    }
    return {ok: true, idx: n};
}

// --------------------------------------------------------------------------
// Ondemand chunk discovery
// --------------------------------------------------------------------------

// escapeRegex escapes regex metacharacters. Used to substitute the chunk
// index into the per-request hash regex (Go uses regexp.QuoteMeta).
function escapeRegex(s) {
    return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function findChunkIndex(homeBody) {
    var m = homeBody.match(ONDEMAND_INDEX_RE);
    if (!m || m.length < 2) {
        throw new Error("could not find ondemand.s chunk index in home html");
    }
    return m[1];
}

function findChunkHash(homeBody, chunkIndex) {
    // Go: `,<index>:"([0-9a-f]+)"` — double quotes only, lowercase hex.
    var pat = new RegExp("," + escapeRegex(chunkIndex) + ':"([0-9a-f]+)"');
    var m = homeBody.match(pat);
    if (!m || m.length < 2) {
        throw new Error("could not find ondemand.s hash for chunk index " + chunkIndex);
    }
    return m[1];
}

// --------------------------------------------------------------------------
// Ondemand JS extraction
// --------------------------------------------------------------------------

function extractIndicesFromOndemand(body) {
    // `g` flag: we use exec() in a loop to collect every capture group 1.
    var re = new RegExp(ONDEMAND_INDICES_RE.source, "g");
    var out = [];
    var match;
    while ((match = re.exec(body)) !== null) {
        if (match.length < 2) {
            continue;
        }
        var n = parseInt(match[1], 10);
        if (isNaN(n)) {
            throw new Error("extract indices: parse int " + JSON.stringify(match[1]));
        }
        out.push(n);
        // Guard against zero-width matches (our pattern always consumes
        // chars, but defensive).
        if (re.lastIndex === match.index) {
            re.lastIndex++;
        }
    }
    if (out.length === 0) {
        throw new Error(
            "extract indices: zero matches in ondemand.js - X likely shipped a " +
            "new bundle, manual re-inspection of the regex required");
    }
    if (out.length < 2) {
        throw new Error("extract indices: need at least 2 indices, got " + out.length);
    }
    return out;
}

// --------------------------------------------------------------------------
// Animation key math (ported from bootstrap.go / Python reference)
// --------------------------------------------------------------------------

// solve maps value (assumed 0..255) linearly into [minVal, maxVal].
// If rounding is true, floors the result; otherwise rounds to two decimal
// places (half-away-from-zero, matching Python's round(x, 2) and Go's
// math.Round behavior for the values this code sees).
function solve(value, minVal, maxVal, rounding) {
    var r = value * (maxVal - minVal) / 255.0 + minVal;
    if (rounding) {
        return Math.floor(r);
    }
    // Math.round in JS rounds half to +infinity, not half-away-from-zero.
    // For positive r this matches; for r*100 producing negatives we use
    // an explicit half-away-from-zero to stay byte-identical to Go's
    // math.Round (which is half-away-from-zero).
    var scaled = r * 100;
    var rounded;
    if (scaled >= 0) {
        rounded = Math.floor(scaled + 0.5);
    } else {
        rounded = -Math.floor(-scaled + 0.5);
    }
    return rounded / 100;
}

// cubicGetValue evaluates cubic-bezier y(x=time) with curves=[ax,ay,bx,by],
// P0=(0,0), P3=(1,1). Mirrors Go exactly, including the t<=0 / t>=1
// boundary handling and the <1e-5 bisection tolerance capped at 60 iters.
function cubicGetValue(curves, t) {
    function calc(a, b, m) {
        return 3 * a * (1 - m) * (1 - m) * m +
               3 * b * (1 - m) * m * m +
               m * m * m;
    }

    if (t <= 0) {
        var g0 = 0.0;
        if (curves[0] > 0) {
            g0 = curves[1] / curves[0];
        } else if (curves[1] === 0 && curves[2] > 0) {
            g0 = curves[3] / curves[2];
        }
        return g0 * t;
    }
    if (t >= 1) {
        var g1 = 0.0;
        if (curves[2] < 1) {
            g1 = (curves[3] - 1) / (curves[2] - 1);
        } else if (curves[2] === 1 && curves[0] < 1) {
            g1 = (curves[1] - 1) / (curves[0] - 1);
        }
        return 1 + g1 * (t - 1);
    }

    var start = 0.0;
    var end = 1.0;
    var mid = 0.0;
    for (var i = 0; i < 60 && start < end; i++) {
        mid = (start + end) / 2.0;
        var x = calc(curves[0], curves[2], mid);
        if (Math.abs(t - x) < 1e-5) {
            return calc(curves[1], curves[3], mid);
        }
        if (x < t) {
            start = mid;
        } else {
            end = mid;
        }
    }
    return calc(curves[1], curves[3], mid);
}

// floatToHex ports the Python reference float_to_hex verbatim (via Go).
// The loop quirk is intentional: for 0 < x < 1 the integer portion is
// empty and the output starts with ".", which callers pad with "0".
// Fractional loop capped at 15 iters (same as Go).
function floatToHex(x) {
    var result = "";
    var quotient = Math.floor(x);
    var fraction = x - quotient;
    while (quotient > 0) {
        quotient = Math.floor(x / 16);
        var remainder = Math.floor(x - quotient * 16);
        if (remainder > 9) {
            // chr(remainder + 55): 10 -> 'A', 15 -> 'F'.
            result = String.fromCharCode(remainder + 55) + result;
        } else {
            result = String.fromCharCode(48 + remainder) + result;
        }
        x = quotient;
    }
    if (fraction === 0) {
        return result;
    }
    result += ".";
    for (var i = 0; i < 15 && fraction > 0; i++) {
        fraction *= 16;
        var integer = Math.floor(fraction);
        fraction -= integer;
        if (integer > 9) {
            result += String.fromCharCode(integer + 55);
        } else {
            result += String.fromCharCode(48 + integer);
        }
    }
    return result;
}

// mathRound: half-away-from-zero, matching Go's math.Round. Used for the
// color byte rounding below. (JS Math.round is half-to-+inf which
// diverges on e.g. -0.5 -> 0 vs Go's -1; we stay exact.)
function mathRound(x) {
    if (x >= 0) {
        return Math.floor(x + 0.5);
    }
    return -Math.floor(-x + 0.5);
}

function computeAnimationKey(keyB64, frames, rowIndex, keyBytesIndices) {
    var keyBytes = base64ToBytes(keyB64);
    if (keyBytes.length === 0) {
        throw new Error("decoded key_bytes is empty");
    }
    if (keyBytes.length <= 5) {
        throw new Error("key_bytes too short: len=" + keyBytes.length + ", need > 5");
    }

    var frameIdx = keyBytes[5] % 4;
    if (frameIdx >= frames.length) {
        throw new Error("frame index " + frameIdx + " out of range (have " + frames.length + " frames)");
    }
    var pathD = frames[frameIdx];
    if (!pathD || pathD.length < 9) {
        throw new Error("path d attribute too short: " + JSON.stringify(pathD));
    }

    // Drop "M...Z " prefix (first 9 chars) and split on "C".
    var tail = pathD.substring(9);
    var segments = tail.split("C");

    var arr = [];
    for (var si = 0; si < segments.length; si++) {
        // Replace non-digit runs with space, collapse whitespace.
        var cleaned = segments[si].replace(NON_DIGIT_RE, " ");
        // Trim and split on any whitespace (Go's strings.Fields).
        cleaned = cleaned.replace(/^\s+|\s+$/g, "");
        if (cleaned.length === 0) {
            continue;
        }
        var fields = cleaned.split(/\s+/);
        var row = [];
        for (var fi = 0; fi < fields.length; fi++) {
            if (fields[fi].length === 0) {
                continue;
            }
            var n = parseInt(fields[fi], 10);
            if (isNaN(n)) {
                continue;
            }
            row.push(n);
        }
        if (row.length > 0) {
            arr.push(row);
        }
    }
    if (arr.length === 0) {
        throw new Error("parsed path d produced no numeric rows");
    }

    if (rowIndex < 0 || rowIndex >= keyBytes.length) {
        throw new Error("rowIndex " + rowIndex + " out of range of key_bytes (len " + keyBytes.length + ")");
    }
    var rowIdxResolved = keyBytes[rowIndex] % 16;
    if (rowIdxResolved >= arr.length) {
        throw new Error("resolved row index " + rowIdxResolved + " >= rows parsed " + arr.length);
    }
    var frameRow = arr[rowIdxResolved];
    if (frameRow.length < 7) {
        throw new Error("frameRow too short: len=" + frameRow.length + ", need >= 7");
    }

    // frame_time = product of (key_bytes[i] % 16) for i in keyBytesIndices.
    var frameTime = 1;
    for (var ki = 0; ki < keyBytesIndices.length; ki++) {
        var idx = keyBytesIndices[ki];
        if (idx < 0 || idx >= keyBytes.length) {
            throw new Error("keyBytesIndices entry " + idx +
                " out of range of key_bytes (len " + keyBytes.length + ")");
        }
        frameTime *= keyBytes[idx] % 16;
    }
    frameTime = mathRound(frameTime / 10) * 10;
    var totalTime = 4096.0;
    var targetTime = frameTime / totalTime;

    var fromColor = [frameRow[0], frameRow[1], frameRow[2], 1.0];
    var toColor = [frameRow[3], frameRow[4], frameRow[5], 1.0];

    var fromRot = 0.0;
    var toRot = solve(frameRow[6], 60.0, 360.0, true);

    // curves from frameRow[7:], alternating lower bounds 0/-1. Pad to 4.
    var curves = [];
    for (var ci = 0; ci < frameRow.length - 7; ci++) {
        var item = frameRow[7 + ci];
        var lo = (ci % 2 === 1) ? -1.0 : 0.0;
        curves.push(solve(item, lo, 1.0, false));
    }
    while (curves.length < 4) {
        curves.push(0.0);
    }

    var val = cubicGetValue(curves, targetTime);

    var color = [0, 0, 0, 0];
    for (var i = 0; i < 4; i++) {
        color[i] = fromColor[i] * (1 - val) + toColor[i] * val;
        if (i < 3) {
            if (color[i] < 0) color[i] = 0;
            if (color[i] > 255) color[i] = 255;
        }
    }

    var rotation = fromRot * (1 - val) + toRot * val;
    var rad = rotation * Math.PI / 180.0;
    var matrix = [Math.cos(rad), -Math.sin(rad), Math.sin(rad), Math.cos(rad)];

    var out = "";
    for (var ci2 = 0; ci2 < 3; ci2++) {
        var rounded = mathRound(color[ci2]);
        // Go: strconv.FormatInt(rounded, 16) then ToLower. JS's toString(16)
        // is already lowercase. Negative values never occur here — color
        // channels are clamped to [0, 255] above — but we mirror Go's
        // FormatInt semantics just in case (it uses a leading "-" for
        // negatives; the final ReplaceAll("-","") below strips those).
        if (rounded < 0) {
            out += "-" + (-rounded).toString(16);
        } else {
            out += rounded.toString(16);
        }
    }
    for (var mi = 0; mi < matrix.length; mi++) {
        var m = matrix[mi];
        // Round to 2 decimal places (Go: math.Round(m*100)/100).
        var r = mathRound(m * 100) / 100;
        if (r < 0) {
            r = -r;
        }
        var h = floatToHex(r);
        // JS's floatToHex uses uppercase A-F (Go: remainder+55 -> 'A'..'F').
        // Go lowercases before appending. Mirror that.
        h = h.toLowerCase();
        if (h.length > 0 && h.charAt(0) === ".") {
            out += "0" + h;
        } else if (h === "") {
            out += "0";
        } else {
            out += h;
        }
    }
    out += "00";
    // Strip "." and "-" from the final string (Go: two ReplaceAll calls).
    out = out.replace(/\./g, "").replace(/-/g, "");
    return out;
}

// --------------------------------------------------------------------------
// Entry point
// --------------------------------------------------------------------------

function bootstrap(input) {
    // input is currently unused — bootstrap is pre-auth and needs nothing
    // from the caller. Accept and ignore to match the runtime contract.
    if (input === undefined || input === null) {
        input = {};
    }

    // 1) Fetch the home page. Follow migration if present.
    var homeResp = httpGet(HOME_URL);
    var homeBody = homeResp.body || "";
    homeBody = maybeFollowMigration(homeBody);
    if (!homeBody || homeBody.length === 0) {
        throw new Error("xcom bootstrap: empty home body");
    }

    // 2) Extract bits from the home HTML.
    var key = extractKey(homeBody);
    var frames = extractFrames(homeBody);

    var chunkIndex = findChunkIndex(homeBody);
    var chunkHash = findChunkHash(homeBody, chunkIndex);

    // 3) Fetch ondemand.js using the hash.
    var ondemandURL = ONDEMAND_URL_TEMPLATE.replace("%s", chunkHash);
    var ondemandResp = httpGet(ondemandURL);
    var ondemandBody = ondemandResp.body || "";
    if (ondemandBody.length === 0) {
        throw new Error("xcom bootstrap: empty ondemand body");
    }

    // 4) Pull indices from ondemand.js.
    var indices = extractIndicesFromOndemand(ondemandBody);
    var rowIndex = indices[0];
    var keyBytesIndices = indices.slice(1);

    // 5) Compute animation_key.
    var animationKey = computeAnimationKey(key, frames, rowIndex, keyBytesIndices);

    // Stringify everything — the host coerces but we're explicit. The
    // additional_random_number is a literal "3" matching the Go State.
    return {
        key_b64: String(key),
        animation_key: String(animationKey),
        additional_random_number: "3"
    };
}
