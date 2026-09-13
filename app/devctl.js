(function () {
  if (!window.scorix || window.__scorixDevCtl) return;
  window.__scorixDevCtl = true;

  // A DOM node or a cycle makes JSON.stringify throw, and the agent asking for a
  // value would get a transport error instead of an answer it can read.
  function plain(v) {
    if (v === undefined) return null;
    try {
      return JSON.parse(JSON.stringify(v));
    } catch (_) {
      return String(v);
    }
  }

  // React and every other controlled-input framework listens for input/change,
  // not for a value assignment, and its own setter is what re-renders the node.
  function setValue(node, v) {
    var proto = node instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    var desc = Object.getOwnPropertyDescriptor(proto, "value");
    if (desc && desc.set) desc.set.call(node, v);
    else node.value = v;
    node.dispatchEvent(new Event("input", { bubbles: true }));
    node.dispatchEvent(new Event("change", { bubbles: true }));
  }

  window.scorix.resolve("dev:eval", function (a) {
    // Indirect eval so the snippet runs against globals instead of this closure.
    return plain((0, eval)(String((a && a.code) || "")));
  });

  window.scorix.resolve("dev:dom", function (a) {
    var sel = String((a && a.selector) || "");
    var all = document.querySelectorAll(sel);
    var limit = Math.min(Number((a && a.limit) || 20) || 20, 200);
    var nodes = [];
    for (var i = 0; i < all.length && nodes.length < limit; i++) {
      var n = all[i];
      var r = n.getBoundingClientRect();
      var attrs = {};
      for (var j = 0; j < n.attributes.length; j++) attrs[n.attributes[j].name] = n.attributes[j].value;
      nodes.push({
        tag: n.tagName.toLowerCase(),
        text: (n.innerText || n.textContent || "").trim().slice(0, 400),
        attrs: attrs,
        rect: { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) },
        // A zero-area box is how "it is in the DOM but nobody can click it" looks.
        visible: !!(r.width && r.height),
      });
    }
    return { count: all.length, nodes: nodes };
  });

  window.scorix.resolve("dev:input", function (a) {
    var sel = String((a && a.selector) || "");
    var node = document.querySelector(sel);
    if (!node) throw new Error("no element matches " + sel);
    var action = String((a && a.action) || "click");
    if (action === "click") {
      node.scrollIntoView({ block: "center" });
      var seq = ["mousedown", "mouseup", "click"];
      for (var i = 0; i < seq.length; i++) {
        node.dispatchEvent(new MouseEvent(seq[i], { bubbles: true, cancelable: true, view: window }));
      }
      return { clicked: sel };
    }
    if (action === "type") {
      var text = String((a && a.text) || "");
      node.focus();
      setValue(node, a && a.clear ? text : (node.value || "") + text);
      return { typed: text.length };
    }
    throw new Error("unknown action: " + action);
  });
})();
