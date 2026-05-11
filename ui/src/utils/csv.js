// 极简 CSV 工具：处理引号、逗号、换行的标准 RFC4180 格式。
// 写入带 UTF-8 BOM，Excel 才不会把中文识别成乱码。

const BOM = '﻿';

// CSV 公式注入防护：Excel/LibreOffice 把以下字符开头的单元格当作公式执行。
// 即使内容来自 admin 自己也要防：admin 看的报表 = admin 自己机器，恶意用户名/标签会变成 RCE。
const FORMULA_TRIGGERS = /^[=+\-@\t\r]/;

// 把任意值转成 CSV 安全字符串
function escapeCell(val) {
  if (val === null || val === undefined) return '';
  let s = typeof val === 'string' ? val : Array.isArray(val) ? val.join('|') : String(val);
  // 公式注入防护：前缀单引号让 Excel 把整个单元格视为字面字符串
  if (FORMULA_TRIGGERS.test(s)) {
    s = "'" + s;
  }
  if (/[",\r\n]/.test(s)) {
    s = '"' + s.replace(/"/g, '""') + '"';
  }
  return s;
}

/**
 * 把对象数组导出成 CSV 字符串。
 * @param {string[]} headers 列名（同时作为对象 key）
 * @param {Array<Record<string, unknown>>} rows
 * @returns {string}
 */
export function toCSV(headers, rows) {
  const headerLine = headers.map(escapeCell).join(',');
  const body = rows.map((r) => headers.map((h) => escapeCell(r[h])).join(','));
  return BOM + [headerLine, ...body].join('\r\n');
}

/**
 * 触发浏览器下载 CSV 文件
 */
export function downloadCSV(filename, content) {
  const blob = new Blob([content], { type: 'text/csv;charset=utf-8;' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

/**
 * 解析 CSV 字符串为对象数组。第一行作为表头。
 * 支持引号包裹、转义双引号、CRLF/LF 换行。
 * @returns {{ headers: string[], rows: Record<string,string>[] }}
 */
export function parseCSV(text) {
  // 去掉 BOM
  if (text.charCodeAt(0) === 0xfeff) text = text.slice(1);

  const cells = [];
  let row = [];
  let cell = '';
  let inQuotes = false;

  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (inQuotes) {
      if (ch === '"') {
        if (text[i + 1] === '"') {
          cell += '"';
          i++;
        } else {
          inQuotes = false;
        }
      } else {
        cell += ch;
      }
      continue;
    }
    if (ch === '"') {
      inQuotes = true;
    } else if (ch === ',') {
      row.push(cell);
      cell = '';
    } else if (ch === '\r') {
      // skip
    } else if (ch === '\n') {
      row.push(cell);
      cell = '';
      cells.push(row);
      row = [];
    } else {
      cell += ch;
    }
  }
  if (cell !== '' || row.length > 0) {
    row.push(cell);
    cells.push(row);
  }

  if (cells.length === 0) return { headers: [], rows: [] };
  const headers = cells[0].map((h) => h.trim());
  const rows = cells.slice(1).filter((r) => r.some((c) => c !== '')).map((r) => {
    const obj = {};
    headers.forEach((h, idx) => { obj[h] = r[idx] ?? ''; });
    return obj;
  });
  return { headers, rows };
}

/**
 * 触发文件选择器，读取 CSV 文件内容
 */
export function pickCSVFile() {
  return new Promise((resolve, reject) => {
    const input = document.createElement('input');
    input.type = 'file';
    input.accept = '.csv,text/csv';
    input.onchange = (e) => {
      const file = e.target.files?.[0];
      if (!file) { reject(new Error('未选择文件')); return; }
      const reader = new FileReader();
      reader.onload = () => resolve(String(reader.result || ''));
      reader.onerror = () => reject(reader.error);
      reader.readAsText(file, 'utf-8');
    };
    input.click();
  });
}
