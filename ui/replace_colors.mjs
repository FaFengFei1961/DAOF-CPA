import fs from 'fs/promises';
import path from 'path';

const TARGET_DIR = './src';

// Map specific hardcoded hex strings to new tailwind MD3 semantic tokens
const REPLACE_MAP = {
  'bg-[#17181c]': 'bg-surface',
  'bg-[#1c1d22]': 'bg-surface',
  'bg-[#25262b]': 'bg-surface-container',
  'bg-[#17171a]': 'bg-surface-container',
  'bg-[#121214]': 'bg-surface-container-high',
  'bg-[#1f2025]': 'bg-surface-container-high',
  'bg-[#2b2b2b]': 'bg-surface-variant',
  'bg-[#333]': 'bg-surface-variant',

  'border-[#2b2b2b]': 'border-outline-variant',
  'border-[#333]': 'border-outline',
  'border-[#373A40]': 'border-outline',
  'border-[#444]': 'border-outline',

  'text-white': 'text-on-surface',
  'text-gray-300': 'text-on-surface-variant',
  'text-gray-400': 'text-on-surface-variant',
  'text-gray-500': 'text-outline',
  'text-gray-600': 'text-outline-variant',

  'text-blue-500': 'text-primary',
  'text-blue-600': 'text-primary',
  'bg-blue-500': 'bg-primary text-on-primary',
  'bg-blue-600': 'bg-primary text-on-primary',
  'hover:bg-blue-500': 'hover:bg-primary-container hover:text-on-primary-container',
  'hover:bg-blue-600': 'hover:bg-primary-container hover:text-on-primary-container',
  'focus:border-blue-500': 'focus:border-primary',
  'border-blue-500': 'border-primary',
};

async function processDirectory(dir) {
  const files = await fs.readdir(dir, { withFileTypes: true });

  for (const file of files) {
    const fullPath = path.join(dir, file.name);

    if (file.isDirectory()) {
      await processDirectory(fullPath);
    } else if (file.isFile() && (fullPath.endsWith('.jsx') || fullPath.endsWith('.js'))) {
      await processFile(fullPath);
    }
  }
}

async function processFile(filePath) {
  let content = await fs.readFile(filePath, 'utf-8');
  let originalContent = content;

  // Simple string replace for every key in the map
  for (const [oldClass, newClass] of Object.entries(REPLACE_MAP)) {
      // Create a global regex that matches the exact class string bounded by spaces/quotes/backticks
      // Because classes are space-separated, we replace instances precisely.
      const regex = new RegExp(`(?<=['"\`\\s])${oldClass.replace(/\[/g, '\\[').replace(/\]/g, '\\]')}(?=['"\`\\s])`, 'g');
      content = content.replace(regex, newClass);
  }

  // Also catch cases at the very beginning or end of lines or strings
  for (const [oldClass, newClass] of Object.entries(REPLACE_MAP)) {
      content = content.split(` ${oldClass} `).join(` ${newClass} `);
      content = content.split(`'${oldClass} `).join(`'${newClass} `);
      content = content.split(`"${oldClass} `).join(`"${newClass} `);
      content = content.split(`\`${oldClass} `).join(`\`${newClass} `);
      content = content.split(` ${oldClass}'`).join(` ${newClass}'`);
      content = content.split(` ${oldClass}"`).join(` ${newClass}"`);
      content = content.split(` ${oldClass}\``).join(` ${newClass}\``);
  }

  if (content !== originalContent) {
    await fs.writeFile(filePath, content, 'utf-8');
    console.log(`Updated: ${filePath}`);
  }
}

processDirectory(TARGET_DIR).then(() => {
  console.log("Migration complete.");
}).catch(e => {
  console.error(e);
});
