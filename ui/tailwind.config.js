module.exports = {
  theme: {
    extend: {
      borderRadius: {
        control: 'var(--radius-control)',
        overlay: 'var(--radius-overlay)',
      },
    },
    borderRadius: {
      control: 'var(--radius-control)',
      overlay: 'var(--radius-overlay)',
      full: '9999px',
      none: '0',
    },
    boxShadow: {
      modal: '0 25px 50px -12px rgb(0 0 0 / 0.4)',
      dropdown: '0 10px 15px -3px rgb(0 0 0 / 0.3)',
      none: 'none',
    },
  },
};
