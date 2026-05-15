export default {
  rules: {
    'strict-tokens': {
      meta: {
        type: 'problem',
        docs: {
          description: 'Enforce design system tokens',
        },
        schema: [],
        messages: {
          noRounded: 'Avoid using default Tailwind rounded classes (like rounded-md, rounded-lg). Use rounded-control or rounded-overlay instead.',
          noHexColor: 'Avoid using hardcoded hex colors. Use design system tokens instead (e.g. bg-surface, text-on-surface).',
          noDefaultColor: 'Avoid using default Tailwind colors. Use semantic tokens (e.g. error, success, warning, primary) instead.',
          noShadow: 'Avoid using shadows unless in Modal/Drawer containers. Do not use default shadow-(sm|md|lg|xl).',
        }
      },
      create(context) {
        function checkValue(node, val) {
          if (/\brounded-(sm|md|lg|xl|2xl|3xl)\b/.test(val)) {
            context.report({ node, messageId: 'noRounded' });
          }
          if (/(bg|text|border)-\[#[0-9a-fA-F]+\]/.test(val)) {
            context.report({ node, messageId: 'noHexColor' });
          }
          if (/\b(bg|text|border)-(red|green|blue|yellow|amber|emerald|rose|indigo|violet|purple|pink|orange|cyan|sky|teal|lime|slate|gray|zinc|neutral|stone)-\d+\b/.test(val)) {
            context.report({ node, messageId: 'noDefaultColor' });
          }
          if (/\bshadow-(sm|md|lg|xl)\b/.test(val)) {
            context.report({ node, messageId: 'noShadow' });
          }
        }
        
        return {
          JSXAttribute(node) {
            if (node.name.name === 'className' && node.value) {
              if (node.value.type === 'Literal' && typeof node.value.value === 'string') {
                checkValue(node, node.value.value);
              } else if (node.value.type === 'JSXExpressionContainer') {
                if (node.value.expression.type === 'TemplateLiteral') {
                  const val = node.value.expression.quasis.map(q => q.value.raw).join(' ');
                  checkValue(node, val);
                } else if (node.value.expression.type === 'Literal' && typeof node.value.expression.value === 'string') {
                  checkValue(node, node.value.expression.value);
                }
              }
            }
          }
        };
      }
    }
  }
};
