// xcom_signer.js
//
// Per-request signer for X / Twitter's `x-client-transaction-id` header.
//
// Algorithm summary
// -----------------
// The Twitter web client computes a per-request opaque token that the edge
// uses to detect unauthorized or replayed automation. The token packs a
// rotating "key" (extracted from the HTML page at session bootstrap), a
// monotonic 4-byte timestamp (seconds since the "Twitter epoch",
// 2023-05-01 UTC), the first 16 bytes of SHA-256("<method>!<path>!<time>
// <random_keyword><animation_key>"), and a small additional byte. The
// whole buffer is then XORed byte-wise with a single random byte which is
// prepended to the output, and the result is base64'd without padding.
//
// This file assumes the caller has already handled bootstrap (the big
// initial fetch of x.com homepage + DOM scraping for the key and the
// animation_key). Those values live on input.state. Everything below is
// just the per-request finalization.
//
// Reference: https://github.com/iSarabjitDhiman/XClientTransaction
// (Python reference implementation; this JS port follows the same math,
//  adapted for our goja sandbox which only exposes hermai.* crypto and
//  base64 helpers that are string-oriented rather than byte-oriented.)
//
// Sandbox notes
// -------------
// - `hermai.sha256(str)` returns a hex string and only accepts strings.
//   We feed it the concatenated message directly because every component
//   of the message (method, path, decimal time, random keyword, animation
//   key) is ASCII-safe, so goja's UTF-16 -> UTF-8 conversion collapses to
//   1 byte per char.
// - `hermai.base64Encode` is string-oriented; for raw bytes we reimplement
//   base64 in pure JS over a byte-number array (0..255).
// - `hermai.randomHex(n)` is our only source of randomness.
//
// No other hermai.* helpers are used in the hot path.

// --------------------------------------------------------------------------
// Constants
// --------------------------------------------------------------------------

var TWITTER_EPOCH_SECONDS = 1682924400;   // 2023-05-01 00:00:00 UTC
var DEFAULT_RANDOM_KEYWORD = "obfiowerehiring";

var B64_ALPHABET =
    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

// --------------------------------------------------------------------------
// Helpers: hex <-> byte array
// --------------------------------------------------------------------------

// hexToBytes: "ab0f" -> [0xab, 0x0f]
// Input must be an even-length lowercase or uppercase hex string. We don't
// validate strictly; callers here always pass hermai.sha256 output.
function hexToBytes(hex) {
    if (typeof hex !== "string") {
        throw new Error("hexToBytes: expected string, got " + typeof hex);
    }
    if (hex.length % 2 !== 0) {
        throw new Error("hexToBytes: odd-length hex string");
    }
    var out = new Array(hex.length / 2);
    for (var i = 0; i < out.length; i++) {
        out[i] = parseInt(hex.substr(i * 2, 2), 16);
    }
    return out;
}

// --------------------------------------------------------------------------
// Helpers: base64 of a byte-number array (standard alphabet, with padding)
// --------------------------------------------------------------------------

// bytesToBase64: takes an array of ints in [0,255], returns a base64 string
// using the standard alphabet and including "=" padding. Callers that want
// unpadded output strip trailing "=" themselves.
function bytesToBase64(bytes) {
    var out = "";
    var len = bytes.length;
    var i = 0;

    // Full 3-byte groups -> 4 chars
    while (i + 3 <= len) {
        var b0 = bytes[i] & 0xFF;
        var b1 = bytes[i + 1] & 0xFF;
        var b2 = bytes[i + 2] & 0xFF;

        out += B64_ALPHABET.charAt(b0 >> 2);
        out += B64_ALPHABET.charAt(((b0 & 0x03) << 4) | (b1 >> 4));
        out += B64_ALPHABET.charAt(((b1 & 0x0F) << 2) | (b2 >> 6));
        out += B64_ALPHABET.charAt(b2 & 0x3F);
        i += 3;
    }

    // Tail: 1 or 2 leftover bytes
    var rem = len - i;
    if (rem === 1) {
        var t0 = bytes[i] & 0xFF;
        out += B64_ALPHABET.charAt(t0 >> 2);
        out += B64_ALPHABET.charAt((t0 & 0x03) << 4);
        out += "==";
    } else if (rem === 2) {
        var s0 = bytes[i] & 0xFF;
        var s1 = bytes[i + 1] & 0xFF;
        out += B64_ALPHABET.charAt(s0 >> 2);
        out += B64_ALPHABET.charAt(((s0 & 0x03) << 4) | (s1 >> 4));
        out += B64_ALPHABET.charAt((s1 & 0x0F) << 2);
        out += "=";
    }

    return out;
}

// --------------------------------------------------------------------------
// Helpers: base64 decode to byte-number array
// --------------------------------------------------------------------------

// base64ToBytes: accepts either standard (+/) or URL-safe (-_) alphabets,
// ignores "=" padding, ignores whitespace. Returns an array of bytes.
function base64ToBytes(s) {
    if (typeof s !== "string") {
        throw new Error("base64ToBytes: expected string, got " + typeof s);
    }

    // Normalize URL-safe to standard, drop whitespace and padding.
    // We build a lookup table once per call; cheap enough and keeps
    // the helper self-contained.
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

    // Tail of 2 or 3 chars (1 or 2 output bytes).
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
// URL -> path (with query)
// --------------------------------------------------------------------------

// urlToPath: strip scheme and host, keeping only the path (+ query).
// We do this manually because the sandbox has no URL parser and we want
// byte-for-byte parity with the Python reference (urlparse .path + query).
function urlToPath(url) {
    if (typeof url !== "string" || url.length === 0) {
        return "/";
    }
    // Strip scheme.
    var schemeIdx = url.indexOf("://");
    var rest = schemeIdx >= 0 ? url.substr(schemeIdx + 3) : url;
    // First "/" after host begins the path.
    var slashIdx = rest.indexOf("/");
    if (slashIdx < 0) {
        return "/";
    }
    // Everything from the slash onward is path+query+fragment. We keep
    // path+query (reference implementation includes query in the hash
    // message; fragments are never sent so this is a no-op for real URLs).
    var tail = rest.substr(slashIdx);
    var fragIdx = tail.indexOf("#");
    if (fragIdx >= 0) {
        tail = tail.substr(0, fragIdx);
    }
    return tail;
}

// --------------------------------------------------------------------------
// Little-endian 4-byte encoding of a non-negative int
// --------------------------------------------------------------------------

function uint32LE(n) {
    // Coerce to uint32 range. The spec only ever feeds this a positive
    // "seconds since epoch" value that's ~1.5e8 at time of writing, well
    // within JS safe integers, so a bitmask is fine.
    return [
        n & 0xFF,
        (n >> 8) & 0xFF,
        (n >> 16) & 0xFF,
        (n >> 24) & 0xFF
    ];
}

// --------------------------------------------------------------------------
// Main entry point
// --------------------------------------------------------------------------

function sign(input) {
    if (!input || typeof input !== "object") {
        throw new Error("sign: input must be an object");
    }
    if (!input.state || typeof input.state !== "object") {
        throw new Error("sign: input.state is required");
    }
    if (!input.url || typeof input.url !== "string") {
        throw new Error("sign: input.url is required");
    }

    var method = (input.method || "GET").toUpperCase();
    var cookies = input.cookies || {};
    var state = input.state;

    // 1) Parse state.
    if (!state.key_b64 || typeof state.key_b64 !== "string") {
        throw new Error("sign: state.key_b64 is required");
    }
    var keyBytes = base64ToBytes(state.key_b64);

    var animationKey = state.animation_key || "";
    if (typeof animationKey !== "string") {
        throw new Error("sign: state.animation_key must be a string");
    }

    var addlRaw = state.additional_random_number;
    var addl;
    if (addlRaw === undefined || addlRaw === null || addlRaw === "") {
        addl = 3;
    } else {
        addl = parseInt(addlRaw, 10);
        if (isNaN(addl)) {
            addl = 3;
        }
    }
    addl = addl & 0xFF;

    // 2) Path component.
    var path = urlToPath(input.url);

    // 3) time_now (seconds since Twitter epoch), little-endian.
    var nowMs = input.now_ms;
    if (typeof nowMs !== "number" || !isFinite(nowMs)) {
        throw new Error("sign: input.now_ms must be a finite number");
    }
    var timeNow = Math.floor((nowMs - TWITTER_EPOCH_SECONDS * 1000) / 1000);
    if (timeNow < 0) {
        // Shouldn't happen with real wall clocks, but don't emit a
        // negative integer: the reference treats it as unsigned.
        timeNow = 0;
    }
    var timeNowBytes = uint32LE(timeNow);

    // 4) SHA-256 of the joined message. All parts are ASCII-safe so the
    //    string -> UTF-8 -> hash pipeline in goja is byte-faithful.
    var msg = method + "!" + path + "!" + String(timeNow) +
              DEFAULT_RANDOM_KEYWORD + animationKey;
    var hashHex = hermai.sha256(msg);
    var hashBytes = hexToBytes(hashHex);
    var hashFirst16 = hashBytes.slice(0, 16);

    // 5) Payload = key || time_now || hash[0:16] || [additional_random_number]
    var payload = [];
    var idx;
    for (idx = 0; idx < keyBytes.length; idx++) {
        payload.push(keyBytes[idx]);
    }
    for (idx = 0; idx < timeNowBytes.length; idx++) {
        payload.push(timeNowBytes[idx]);
    }
    for (idx = 0; idx < hashFirst16.length; idx++) {
        payload.push(hashFirst16[idx]);
    }
    payload.push(addl);

    // 6) Random byte in [0, 255].
    var randHex = hermai.randomHex(1);
    var randNum = parseInt(randHex, 16) & 0xFF;
    if (isNaN(randNum)) {
        // randomHex should never return non-hex, but fail closed.
        throw new Error("sign: hermai.randomHex returned invalid hex");
    }

    // 7) XOR payload with randNum, prefix with randNum.
    var out = new Array(payload.length + 1);
    out[0] = randNum;
    for (idx = 0; idx < payload.length; idx++) {
        out[1 + idx] = (payload[idx] ^ randNum) & 0xFF;
    }

    // 8) Base64 encode, strip trailing "=".
    var b64 = bytesToBase64(out);
    while (b64.length > 0 && b64.charAt(b64.length - 1) === "=") {
        b64 = b64.substr(0, b64.length - 1);
    }

    // 9) Return the header set. The caller merges these onto input.headers.
    return {
        url: input.url,
        headers: {
            "x-client-transaction-id": b64,
            "x-csrf-token": cookies.ct0 || ""
        }
    };
}
