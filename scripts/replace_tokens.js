const fs = require('fs');
const path = require('path');

function getFiles(dir, exts) {
  let results = [];
  const list = fs.readdirSync(dir);
  list.forEach(file => {
    file = path.join(dir, file);
    const stat = fs.statSync(file);
    if (stat && stat.isDirectory()) {
      results = results.concat(getFiles(file, exts));
    } else {
      if (exts.some(ext => file.endsWith(ext))) {
        results.push(file);
      }
    }
  });
  return results;
}

const files = getFiles('ui/src', ['.jsx', '.js', '.tsx', '.ts']);
let stats = {
  rounded: 0,
  shadow: 0,
  hex: 0,
  colors: 0,
  filesModified: 0,
  linesModified: 0
};

files.forEach(f => {
  // Exclusions
  if (f.includes(path.join('components', 'ui')) || f.includes('ConfirmContext.jsx')) return;

  let content = fs.readFileSync(f, 'utf8');
  let lines = content.split('\n');
  let newLines = [];
  let fileModified = false;

  for (let i = 0; i < lines.length; i++) {
    let line = lines[i];
    let originalLine = line;

    // B1: Rounded
    let r1 = line.match(/\brounded-(lg|md)\b/g);
    if (r1) { stats.rounded += r1.length; line = line.replace(/\brounded-(lg|md)\b/g, 'rounded-control'); }
    
    let r2 = line.match(/\brounded-(xl|2xl|3xl)\b/g);
    if (r2) { stats.rounded += r2.length; line = line.replace(/\brounded-(xl|2xl|3xl)\b/g, 'rounded-overlay'); }
    
    // only replace whole word 'rounded' but not 'rounded-full' or 'rounded-t' etc
    // \brounded\b requires it to not have a dash after it.
    // wait, what about 'rounded '? it works. What about 'rounded"'? it works.
    let r3 = line.match(/\brounded\b/g);
    if (r3) { stats.rounded += r3.length; line = line.replace(/\brounded\b/g, 'rounded-control'); }

    // B2: Shadows
    let s1 = line.match(/\bshadow-(sm|md|lg|xl)\b/g);
    if (s1) { stats.shadow += s1.length; line = line.replace(/\bshadow-(sm|md|lg|xl)\b/g, ''); }
    
    let s2 = line.match(/\bshadow-[a-zA-Z]+-[0-9]+\b/g);
    if (s2) { stats.shadow += s2.length; line = line.replace(/\bshadow-[a-zA-Z]+-[0-9]+\b/g, ''); }
    
    let s3 = line.match(/\bshadow-\[[^\]]+\]/g);
    if (s3) { stats.shadow += s3.length; line = line.replace(/\bshadow-\[[^\]]+\]/g, ''); }

    let s4 = line.match(/\bshadow-2xl\b/g);
    if (s4 && !line.includes('shadow-black/40')) {
      line = line.replace(/\bshadow-2xl\b/g, 'shadow-2xl shadow-black/40');
    }

    // Clean up multiple spaces inside quotes
    line = line.replace(/className=(["'`])(.*?)(\1)/g, (match, p1, p2, p3) => {
        return `className=${p1}${p2.replace(/\s{2,}/g, ' ')}${p3}`;
    });

    // B3: Hex Colors
    const hexMap = {
      'bg-[#1a1b1e]': 'bg-surface',
      'bg-[#1a1c1e]': 'bg-surface',
      'bg-[#1e1e20]': 'bg-surface-container',
      'bg-[#2b2b2b]': 'bg-surface-container-high',
      'bg-[#2c2d31]': 'bg-surface-container-high',
      'bg-[#3f4148]': 'bg-surface-variant',
      'bg-[#1a1515]': 'bg-surface',
      'border-[#3f4148]': 'border-outline-variant',
      'border-[#2b2b2b]': 'border-outline-variant'
    };

    for (let [key, val] of Object.entries(hexMap)) {
      if (line.includes(key)) {
        let count = line.split(key).length - 1;
        stats.hex += count;
        line = line.split(key).join(val);
      }
    }
    
    let h1 = line.match(/text-\[#[0-9a-fA-F]+\]/g);
    if (h1) {
      stats.hex += h1.length;
      line = line.replace(/text-\[#[0-9a-fA-F]+\]/g, 'text-on-surface');
    }

    // B4: Tailwind Colors
    const colorMap = {
      'red': 'error',
      'rose': 'error',
      'emerald': 'success',
      'green': 'success',
      'teal': 'success',
      'amber': 'warning',
      'yellow': 'warning',
      'orange': 'warning',
      'blue': 'primary',
      'sky': 'primary',
      'cyan': 'primary',
      'indigo': 'primary',
      'violet': 'primary',
      'gray': 'surface-variant',
      'slate': 'surface-variant',
      'zinc': 'surface-variant',
      'neutral': 'surface-variant',
      'stone': 'surface-variant'
    };

    let colorRegex = /\b(bg|text|border)-(red|rose|emerald|green|teal|amber|yellow|orange|blue|sky|cyan|indigo|violet|gray|slate|zinc|neutral|stone)-[0-9]+(\/[0-9]+)?\b/g;
    
    let c1 = line.match(colorRegex);
    if (c1) {
      stats.colors += c1.length;
      line = line.replace(colorRegex, (fullMatch, prefix, color, opacity) => {
        opacity = opacity || '';
        let mappedToken = colorMap[color];
        let number = parseInt(fullMatch.split('-')[2].split('/')[0], 10);
        if (['gray', 'slate', 'zinc', 'neutral', 'stone'].includes(color)) {
          if (prefix === 'bg') {
            if (number <= 200) mappedToken = 'surface-container';
            else if (number >= 700) mappedToken = 'surface-container-high';
            else mappedToken = 'surface-variant';
          } else if (prefix === 'text') {
            mappedToken = 'on-surface-variant';
          } else if (prefix === 'border') {
            mappedToken = 'outline-variant';
          }
        }
        return `${prefix}-${mappedToken}${opacity}`;
      });
    }

    // Trim trailing spaces if any
    line = line.replace(/\s+$/, '');

    newLines.push(line);
    if (line !== originalLine) {
      fileModified = true;
      stats.linesModified++;
    }
  }

  if (fileModified) {
    fs.writeFileSync(f, newLines.join('\n'), 'utf8');
    stats.filesModified++;
  }
});

console.log(JSON.stringify(stats, null, 2));
