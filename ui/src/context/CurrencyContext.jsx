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
                if (data.success && data.exchange_rate) {
                    const parsed = parseFloat(data.exchange_rate);
                    if (!isNaN(parsed) && parsed > 0) {
                        setExchangeRate(parsed);
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
