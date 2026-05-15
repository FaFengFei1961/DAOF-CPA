#!/usr/bin/env node

const fs = require('fs');
const path = require('path');

const ROOT = path.resolve(__dirname, '..');
const LOCALE_FILES = {
  zh: path.join(ROOT, 'i18n', 'zh-CN.json'),
  en: path.join(ROOT, 'i18n', 'en-US.json'),
};
const UI_SRC = path.join(ROOT, 'ui', 'src');
const SCAN_EXTENSIONS = new Set(['.js', '.jsx']);

function readJson(filePath) {
  return JSON.parse(fs.readFileSync(filePath, 'utf8'));
}

function flatten(value, prefix = '', output = new Map()) {
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    for (const [key, child] of Object.entries(value)) {
      flatten(child, prefix ? `${prefix}.${key}` : key, output);
    }
    return output;
  }
  output.set(prefix, value);
  return output;
}

function collectFiles(dir, output = []) {
  if (!fs.existsSync(dir)) return output;

  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      collectFiles(fullPath, output);
      continue;
    }
    if (entry.isFile() && SCAN_EXTENSIONS.has(path.extname(entry.name))) {
      output.push(fullPath);
    }
  }
  return output;
}

function lineNumber(source, index) {
  return source.slice(0, index).split(/\r?\n/).length;
}

function nextSignificantChar(source, index) {
  for (let i = index; i < source.length; i += 1) {
    if (!/\s/.test(source[i])) return source[i];
  }
  return '';
}

function recordDynamicPrefix(key, usedPrefixes) {
  const templateIndex = key.indexOf('${');
  if (templateIndex > 0) {
    usedPrefixes.add(key.slice(0, templateIndex));
    return true;
  }
  return false;
}

function collectCodeKeys() {
  const usedKeys = new Map();
  const usedPrefixes = new Set();
  const callPattern = /(?:^|[^\w$.])(?:i18n\.)?t\s*\(\s*(['"`])([^'"`]*?)\1/g;

  for (const filePath of collectFiles(UI_SRC)) {
    const source = fs.readFileSync(filePath, 'utf8');
    let match;
    while ((match = callPattern.exec(source)) !== null) {
      const quote = match[1];
      const key = match[2];
      const keyStart = match.index + match[0].lastIndexOf(key);
      const keyEnd = keyStart + key.length + 1;

      if (!key || recordDynamicPrefix(key, usedPrefixes)) {
        continue;
      }

      if (quote === '`' && key.includes('${')) {
        recordDynamicPrefix(key, usedPrefixes);
        continue;
      }

      const nextChar = nextSignificantChar(source, keyEnd);
      if (nextChar === '+') {
        usedPrefixes.add(key);
        continue;
      }

      const relativePath = path.relative(ROOT, filePath).replace(/\\/g, '/');
      if (!usedKeys.has(key)) usedKeys.set(key, []);
      usedKeys.get(key).push(`${relativePath}:${lineNumber(source, match.index)}`);
    }
  }

  return { usedKeys, usedPrefixes };
}

function formatList(title, values) {
  if (values.length === 0) return;
  console.warn(`\n${title}`);
  for (const value of values) {
    console.warn(`  - ${value}`);
  }
}

function main() {
  const zh = flatten(readJson(LOCALE_FILES.zh));
  const en = flatten(readJson(LOCALE_FILES.en));
  const zhKeys = new Set(zh.keys());
  const enKeys = new Set(en.keys());
  const allKeys = new Set([...zhKeys, ...enKeys]);

  const missingInEn = [...zhKeys].filter((key) => !enKeys.has(key)).sort();
  const missingInZh = [...enKeys].filter((key) => !zhKeys.has(key)).sort();
  const { usedKeys, usedPrefixes } = collectCodeKeys();
  const missingInJson = [...usedKeys.keys()]
    .filter((key) => !allKeys.has(key))
    .sort()
    .map((key) => `${key} (${usedKeys.get(key).join(', ')})`);
  const orphanKeys = [...allKeys]
    .filter((key) => !usedKeys.has(key))
    .filter((key) => ![...usedPrefixes].some((prefix) => key.startsWith(prefix)))
    .sort();

  let failed = false;

  if (missingInEn.length > 0 || missingInZh.length > 0) {
    failed = true;
    formatList('Keys present in zh-CN.json but missing in en-US.json:', missingInEn);
    formatList('Keys present in en-US.json but missing in zh-CN.json:', missingInZh);
  }

  if (missingInJson.length > 0) {
    failed = true;
    formatList('Code references missing from locale JSON:', missingInJson);
  }

  formatList('Locale keys not referenced by literal t(...) calls:', orphanKeys);

  if (failed) {
    process.exit(1);
  }

  console.log('i18n check passed.');
}

main();
