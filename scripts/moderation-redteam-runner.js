#!/usr/bin/env node
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import process from 'node:process';

const defaults = {
  baseUrl: process.env.DAOFA_BASE_URL || 'http://127.0.0.1:3000',
  cases: path.join('scripts', 'moderation-redteam-cases.json'),
  model: process.env.DAOFA_MODERATION_TEST_MODEL || 'gpt-5.5',
  includeSmart: false,
};

function parseArgs(argv) {
  const out = { ...defaults };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    const next = () => {
      i += 1;
      if (i >= argv.length) throw new Error(`Missing value for ${arg}`);
      return argv[i];
    };
    if (arg === '--base-url') out.baseUrl = next();
    else if (arg === '--cases') out.cases = next();
    else if (arg === '--model') out.model = next();
    else if (arg === '--include-smart') out.includeSmart = true;
    else if (arg === '--help' || arg === '-h') out.help = true;
    else throw new Error(`Unknown argument: ${arg}`);
  }
  return out;
}

function usage() {
  return `Usage:
  node scripts/moderation-redteam-runner.js [--base-url http://127.0.0.1:3000] [--model gpt-5.5] [--cases path] [--include-smart]

Environment:
  DAOFA_ADMIN_TOKEN   Admin bearer token. If omitted, the runner tries local sqlite3 data/daofa-hub.db.
  DAOFA_BASE_URL      Default base URL.

Expect values:
  allow | block | model_review | score_only | not_allow | no_block
`;
}

function readAdminToken() {
  const envToken = (process.env.DAOFA_ADMIN_TOKEN || '').trim();
  if (envToken) return envToken;
  try {
    const token = execFileSync('sqlite3', [
      path.join('data', 'daofa-hub.db'),
      "SELECT token FROM users WHERE role='admin' AND status=1 ORDER BY id LIMIT 1;",
    ], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim();
    if (token) return token;
  } catch {
    // Fall through to a clear error below.
  }
  throw new Error('DAOFA_ADMIN_TOKEN is not set and admin token could not be read with sqlite3.');
}

function loadCases(file) {
  const raw = readFileSync(file, 'utf8');
  const parsed = JSON.parse(raw);
  if (!Array.isArray(parsed)) throw new Error('Cases file must contain a JSON array.');
  return parsed.map((item, idx) => {
    if (!item.id || !item.text || !item.expect) {
      throw new Error(`Case at index ${idx} must include id, text, and expect.`);
    }
    return item;
  });
}

function passForExpectation(expect, decision) {
  switch (expect) {
    case 'allow':
    case 'block':
    case 'model_review':
    case 'score_only':
      return decision === expect;
    case 'not_allow':
      return decision !== 'allow';
    case 'no_block':
      return decision !== 'block';
    default:
      throw new Error(`Unknown expect value: ${expect}`);
  }
}

async function evaluateCase({ baseUrl, token, model, includeSmart, item }) {
  const response = await fetch(`${baseUrl.replace(/\/$/, '')}/api/admin/moderation/evaluate`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${token}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      model: item.model || model,
      text: item.text,
      include_smart: item.include_smart ?? includeSmart,
    }),
  });
  const body = await response.json().catch(() => ({}));
  if (!response.ok || !body.success) {
    throw new Error(`HTTP ${response.status}: ${JSON.stringify(body)}`);
  }
  return body;
}

function printRow(row) {
  const icon = row.pass ? 'PASS' : 'FAIL';
  const bits = [
    icon.padEnd(4),
    row.id.padEnd(34),
    `expect=${row.expect.padEnd(12)}`,
    `got=${row.decision.padEnd(12)}`,
    `reason=${row.reason || '-'}`,
  ];
  if (row.action) bits.push(`action=${row.action}`);
  if (row.keyword) bits.push(`keyword=${row.keyword}`);
  console.log(bits.join('  '));
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.help) {
    console.log(usage());
    return;
  }
  const token = readAdminToken();
  const cases = loadCases(args.cases);
  const rows = [];
  for (const item of cases) {
    const result = await evaluateCase({ ...args, token, item });
    const decision = result.decision || 'unknown';
    rows.push({
      id: item.id,
      title: item.title || item.id,
      expect: item.expect,
      decision,
      reason: result.reason,
      action: result.action,
      keyword: result.keyword,
      pass: passForExpectation(item.expect, decision),
      result,
    });
  }

  rows.forEach(printRow);
  const failed = rows.filter(r => !r.pass);
  console.log('');
  console.log(`Moderation redteam regression: ${rows.length - failed.length}/${rows.length} passed`);
  if (failed.length > 0) {
    console.log('\nFailures:');
    failed.forEach(row => {
      console.log(`- ${row.id}: expected ${row.expect}, got ${row.decision}`);
      console.log(`  ${JSON.stringify(row.result)}`);
    });
    process.exitCode = 1;
  }
}

main().catch(err => {
  console.error(`moderation-redteam-runner failed: ${err.message}`);
  process.exit(1);
});
