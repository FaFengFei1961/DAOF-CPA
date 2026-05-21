import React, { createContext, useContext, useState, useEffect } from 'react';

const CurrencyContext = createContext(null);

export const useCurrency = () => {
    const context = useContext(CurrencyContext);
    if (!context) {
        throw new Error('useCurrency must be used within a CurrencyProvider');
    }
    return context;
};

export const CurrencyProvider = ({ children }) => {
    const [exchangeRate, setExchangeRate] = useState(1.0);
    const [displayCurrency, setDisplayCurrency] = useState(() => {
        return localStorage.getItem('daof_display_currency') || 'USD';
    });
    const [loading, setLoading] = useState(true);

    useEffect(() => {
        const fetchConfig = async () => {
            try {
                const res = await fetch('/api/public-config');
                const data = await res.json();
                // fix Sprint4-M3：协议从 exchange_rate (float string) 改为
                // exchange_rate_rmb_per_usd_micros (int64 string, RMB/USD × 1e6)
                if (data.success && data.exchange_rate_rmb_per_usd_micros) {
                    const micros = parseInt(data.exchange_rate_rmb_per_usd_micros, 10);
                    if (Number.isFinite(micros) && micros > 0) {
                        setExchangeRate(micros / 1_000_000);
                    }
                }
            } catch {
                /* fetch failed → use default exchange rate */
            } finally {
                setLoading(false);
            }
        };
        fetchConfig();
    }, []);

    const toggleCurrency = () => {
        setDisplayCurrency(prev => {
            const next = prev === 'USD' ? 'CNY' : 'USD';
            localStorage.setItem('daof_display_currency', next);
            return next;
        });
    };

    /**
     * IA audit M-V3/M-V4/M-V5/M-V6 fix.
     *
     * formatCurrency is the single source of truth for currency display:
     *
     *   - Trailing zeros are preserved (previous Number(toFixed) collapsed
     *     $1.00 → $1 and $0.10 → $0.1, breaking stable layout).
     *   - Tiered decimals when the caller does not pin a value: large amounts
     *     (≥ $1) get 2 digits; small amounts get 4; very-small amounts get 6.
     *     Was admin-only via makeFormatMeterCost; now usable on Dashboard,
     *     Topup, BillsPage, AdminTopupOrders, audit pages, etc.
     *   - Thousand-separator via toLocaleString.
     *
     * Callers that need an exact width pass `decimals` explicitly. Callers
     * that pass nothing (default) get the tier rule.
     */
    const pickDecimalsForUSD = (usd) => {
        const abs = Math.abs(usd);
        if (abs === 0) return 2;
        if (abs >= 1) return 2;
        if (abs >= 0.001) return 4;
        return 6;
    };

    const formatNumberWithDecimals = (value, locale, decimals) => (
        value.toLocaleString(locale, {
            minimumFractionDigits: decimals,
            maximumFractionDigits: decimals,
        })
    );

    const formatCurrency = (usdAmount, decimals) => {
        if (typeof usdAmount !== 'number' || !Number.isFinite(usdAmount)) return usdAmount;
        const usdDecimals = typeof decimals === 'number' ? decimals : pickDecimalsForUSD(usdAmount);

        if (displayCurrency === 'CNY') {
            const cnyAmount = usdAmount * exchangeRate;
            // For CNY rendering we use the same number of digits as the USD
            // tier so the table column width stays stable when toggling currency.
            return `￥${formatNumberWithDecimals(cnyAmount, 'zh-CN', usdDecimals)}`;
        }
        return `$${formatNumberWithDecimals(usdAmount, 'en-US', usdDecimals)}`;
    };

    // formatCurrencyFixed is the "no smart tier" entry point — useful when the
    // caller already knows the exact decimal count it needs (e.g. table column
    // alignment that must be uniform regardless of value).
    const formatCurrencyFixed = (usdAmount, decimals = 3) => formatCurrency(usdAmount, decimals);

    return (
        <CurrencyContext.Provider value={{ exchangeRate, displayCurrency, toggleCurrency, formatCurrency, formatCurrencyFixed, loading }}>
            {children}
        </CurrencyContext.Provider>
    );
};
