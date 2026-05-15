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

    const formatCurrency = (usdAmount, maxDecimals = 3) => {
        if (typeof usdAmount !== 'number' || !Number.isFinite(usdAmount)) return usdAmount;

        if (displayCurrency === 'CNY') {
            const cnyAmount = usdAmount * exchangeRate;
            return `￥${Number(cnyAmount.toFixed(maxDecimals))}`;
        }
        return `$${Number(usdAmount.toFixed(maxDecimals))}`;
    };

    const formatCurrencyFixed = (usdAmount, decimals = 3) => {
        if (typeof usdAmount !== 'number' || !Number.isFinite(usdAmount)) return usdAmount;

        if (displayCurrency === 'CNY') {
            return `￥${(usdAmount * exchangeRate).toFixed(decimals)}`;
        }
        return `$${usdAmount.toFixed(decimals)}`;
    };

    return (
        <CurrencyContext.Provider value={{ exchangeRate, displayCurrency, toggleCurrency, formatCurrency, formatCurrencyFixed, loading }}>
            {children}
        </CurrencyContext.Provider>
    );
};
