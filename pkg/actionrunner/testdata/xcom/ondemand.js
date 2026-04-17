/*! ondemand.s fake fixture */
(function(){"use strict";
// Obfuscated blob with the (w[N], 16) pattern we key on. Indices
// chosen so that all values are within range of the key_bytes below
// (key_bytes has >= 30 bytes).
var a = function(w){
    var x = (parseInt(w[7], 16));
    var y = (parseInt(w[11], 16));
    var z = (parseInt(w[2], 16));
    var q = (parseInt(w[23], 16));
    var r = (parseInt(w[14], 16));
    return (x ^ y) + (z ^ q) + r;
};
// Another nested chunk with more (w[NN], 16) hits:
function b(w){
    return (parseInt(w[9], 16)) * (parseInt(w[5], 16)) + (parseInt(w[17], 16));
}
window.__ondemand__ = {a: a, b: b};
})();
