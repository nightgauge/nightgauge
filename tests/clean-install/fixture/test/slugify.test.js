"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { slugify } = require("../src");

test("lower-cases and hyphenates words", () => {
  assert.equal(slugify("Hello World"), "hello-world");
});

test("collapses runs of punctuation and whitespace", () => {
  assert.equal(slugify("  a -- b ,, c  "), "a-b-c");
});

test("strips diacritics", () => {
  assert.equal(slugify("Crème brûlée"), "creme-brulee");
});

test("rejects non-strings", () => {
  assert.throws(() => slugify(42), TypeError);
});
