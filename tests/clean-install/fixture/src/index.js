"use strict";

/**
 * Turn free text into a URL-safe slug: lower-case, ASCII letters and digits
 * only, words joined by single hyphens, no leading or trailing hyphen.
 */
function slugify(text) {
  if (typeof text !== "string") {
    throw new TypeError("slugify: text must be a string");
  }
  return text
    .normalize("NFKD")
    .replace(/[̀-ͯ]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

module.exports = { slugify };
