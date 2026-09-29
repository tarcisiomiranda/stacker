import fs from "node:fs";
import vm from "node:vm";
import assert from "node:assert/strict";

const html = fs.readFileSync("cmd/stacker/templates/logs.html", "utf8");
const source = html.match(/<script id="sidebar-resize-script">([\s\S]*?)<\/script>/)?.[1];
if (!source) throw new Error("resize controller is missing");

const values = new Map();

function setup(config, blocked = false) {
  const events = {};
  const windowEvents = {};
  const styles = {};
  const attributes = {};
  const separator = {
    addEventListener(name, handler) { events[name] = handler; },
    setAttribute(name, value) { attributes[name] = value; },
    setPointerCapture(id) { this.pointerId = id; },
    releasePointerCapture() { this.pointerId = null; },
  };
  const document = {
    getElementById(id) { return id === "sidebar-resize" ? separator : null; },
    documentElement: { style: { setProperty(name, value) { styles[name] = value; } } },
  };
  const window = {
    innerWidth: 1000,
    addEventListener(name, handler) { windowEvents[name] = handler; },
  };
  const localStorage = {
    getItem(key) { if (blocked) throw new Error("storage blocked"); return values.get(key) ?? null; },
    setItem(key, value) { if (blocked) throw new Error("storage blocked"); values.set(key, value); },
  };
  vm.runInNewContext(source.replace(/\{\{\.Config\}\}/g, JSON.stringify(config)), { document, window, localStorage, Math, Number, JSON });
  return { events, windowEvents, styles, attributes, separator, window };
}

const first = setup("/tmp/first/stacker.yml");
assert.equal(first.styles["--sidebar-width"], "224px");
first.events.pointerdown({ pointerId: 1, clientX: 100, preventDefault() {} });
first.events.pointermove({ pointerId: 1, clientX: 160 });
assert.equal(first.styles["--sidebar-width"], "284px");
first.events.pointerup({ pointerId: 1, clientX: 160 });
assert.equal(values.get("stacker:sidebar-width:/tmp/first/stacker.yml"), "284");
first.events.keydown({ key: "ArrowLeft", preventDefault() {} });
assert.equal(first.styles["--sidebar-width"], "268px");
first.events.keydown({ key: "Home", preventDefault() {} });
assert.equal(first.styles["--sidebar-width"], "224px");
first.events.pointerdown({ pointerId: 2, clientX: 50, preventDefault() {} });
first.events.pointermove({ pointerId: 2, clientX: 120 });
first.events.pointercancel({ pointerId: 2 });
assert.equal(first.styles["--sidebar-width"], "224px");
first.window.innerWidth = 430;
first.windowEvents.resize();
assert.equal(first.styles["--sidebar-width"], "160px");

const second = setup("/tmp/second/stacker.yml");
assert.equal(second.styles["--sidebar-width"], "224px");
second.events.keydown({ key: "ArrowRight", preventDefault() {} });
assert.equal(values.get("stacker:sidebar-width:/tmp/second/stacker.yml"), "240");

const unavailable = setup("/tmp/unavailable/stacker.yml", true);
unavailable.events.pointerdown({ pointerId: 3, clientX: 100, preventDefault() {} });
unavailable.events.pointermove({ pointerId: 3, clientX: 200 });
unavailable.events.pointerup({ pointerId: 3, clientX: 200 });
assert.equal(unavailable.styles["--sidebar-width"], "324px");
unavailable.events.keydown({ key: "ArrowLeft", preventDefault() {} });
assert.equal(unavailable.styles["--sidebar-width"], "308px");

const cancelled = setup("/tmp/cancelled/stacker.yml");
cancelled.separator.releasePointerCapture = () => { throw new Error("pointer capture already released"); };
cancelled.events.pointerdown({ pointerId: 4, clientX: 100, preventDefault() {} });
cancelled.events.pointercancel({ pointerId: 4 });
cancelled.events.pointerdown({ pointerId: 5, clientX: 100, preventDefault() {} });
cancelled.events.pointermove({ pointerId: 5, clientX: 130 });
assert.equal(cancelled.styles["--sidebar-width"], "254px");
