import React, { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { Upload, Download, Trash2, Globe, FileJson, RefreshCw, Plus } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch } from '../utils/authFetch';

const I18nManagement = () => {
    const confirm = useConfirm();
    const { t } = useTranslation();
    const [locales, setLocales] = useState([]);
    const [loading, setLoading] = useState(true);
    const [uploading, setUploading] = useState(false);

    const fetchLocales = async () => {
        setLoading(true);
        try {
            const res = await fetch('/api/i18n/locales');
            const data = await res.json();
            if (data.success) {
                setLocales(data.data || []);
            }
        } catch (e) {
            toast.error(t('I18N_MGMT.FETCH_FAILED'));
        }
        setLoading(false);
    };

    useEffect(() => {
        fetchLocales();
    }, []);

    const handleFileUpload = async (e) => {
        const file = e.target.files[0];
        if (!file) return;

        if (!file.name.endsWith('.json')) {
            toast.error(t('I18N_MGMT.ONLY_JSON'));
            return;
        }

        const langId = file.name.replace('.json', '');

        try {
            setUploading(true);
            const text = await file.text();
            let jsonData;
            try {
                jsonData = JSON.parse(text);
            } catch (err) {
                toast.error(t('I18N_MGMT.INVALID_JSON'));
                return;
            }

            // fix MAJOR（多模型审计第二十五轮 P2）：admin 写操作改 authFetch，统一鉴权 + 错误归一化
            const data = await authFetch(`/api/admin/i18n/${langId}`, {
                method: 'POST',
                body: jsonData,
            });

            if (data.success) {
                toast.success(t('I18N_MGMT.UPLOAD_SUCCESS', { langId }));
                fetchLocales();
            } else {
                toast.error((data.message_code ? t('API.' + data.message_code) : data.message));
            }

        } catch (err) {
            /* error swallowed */;
            toast.error(t('I18N_MGMT.UPLOAD_ERROR'));
        } finally {
            setUploading(false);
            e.target.value = ''; // Reset input
        }
    };

    const handleDelete = async (langId) => {
        if (!(await confirm(t('I18N_MGMT.DELETE_CONFIRM', { langId })))) return;

        try {
            // fix MAJOR（多模型审计第二十五轮 P2）：admin 写操作改 authFetch
            const data = await authFetch(`/api/admin/i18n/${langId}`, { method: 'DELETE' });

            if (data.success) {
                toast.success(t('I18N_MGMT.DELETE_SUCCESS'));
                fetchLocales();
            } else {
                toast.error((data.message_code ? t('API.' + data.message_code) : data.message));
            }
        } catch (e) {
            toast.error(t('I18N_MGMT.DELETE_NET_ERROR'));
        }
    };

    const triggerDownload = (langId) => {
        const url = `/api/i18n/locales/${langId}`;
        const a = document.createElement('a');
        a.href = url;
        a.download = `${langId}.json`;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
    };

    return (
        <div className="w-full">
            <div className="mb-8 border-b border-outline-variant pb-6 flex items-center justify-between">
                <div>
                    <h1 className="text-2xl font-bold tracking-tight text-on-surface flex items-center gap-3">
                        {t('I18N_MGMT.TITLE')}
                    </h1>
                    <p className="text-on-surface-variant mt-2 text-sm">
                        {t('I18N_MGMT.DESC')}
                    </p>
                </div>
                <div>
                   <label className="cursor-pointer bg-gradient-to-r from-blue-600 to-indigo-600 text-on-surface px-5 py-2.5 rounded-overlay font-medium flex items-center gap-2 hover:opacity-90 /20 ">
                       {uploading ? <RefreshCw size={18} className="animate-spin" /> : <Upload size={18} />}
                       <span>{t('I18N_MGMT.BTN_UPLOAD')}</span>
                       <input type="file" accept=".json" className="hidden" onChange={handleFileUpload} disabled={uploading} />
                   </label>
                </div>
            </div>

            <div className="fl-table-shell">
                <div className="fl-table-scroll">
                    <table className="w-full min-w-[800px] text-left text-sm text-on-surface-variant table-fixed">
                        <thead className="bg-surface-container-high text-xs uppercase font-mono tracking-wider text-on-surface-variant border-b border-outline-variant">
                            <tr>
                                <th className="px-6 py-4 font-medium w-[20%]">{t('I18N_MGMT.TABLE_ID')}</th>
                                <th className="px-6 py-4 font-medium w-[40%]">{t('I18N_MGMT.TABLE_NAME')}</th>
                                <th className="px-6 py-4 font-medium w-[20%]">{t('I18N_MGMT.TABLE_SIZE')}</th>
                                <th className="px-6 py-4 font-medium text-right w-[20%]">{t('I18N_MGMT.TABLE_ACTIONS')}</th>
                            </tr>
                        </thead>
                        <tbody className="divide-y divide-outline-variant/50">
                            {loading ? (
                                <tr>
                                    <td colSpan="4" className="px-6 py-12 text-center text-on-surface-variant">{t('I18N_MGMT.LOADING')}</td>
                                </tr>
                            ) : locales.length === 0 ? (
                                <tr>
                                    <td colSpan="4" className="px-6 py-12 text-center text-on-surface-variant">{t('I18N_MGMT.EMPTY')}</td>
                                </tr>
                            ) : (
                                locales.map(loc => {
                                    const isCore = loc.id === 'zh-CN' || loc.id === 'en-US';
                                    return (
                                        <tr key={loc.id} className="hover:bg-surface group">
                                            <td className="px-6 py-4">
                                                <div className="flex items-center gap-2">
                                                    <FileJson size={16} className={isCore ? 'text-warning' : 'text-primary'} />
                                                    <span className="text-on-surface font-mono font-medium">{loc.id}.json</span>
                                                    {isCore && <span className="text-xs bg-warning/30 text-warning border border-warning/50 px-1.5 py-0.5 rounded-control ml-2 uppercase font-bold tracking-widest">{t('I18N_MGMT.CORE')}</span>}
                                                </div>
                                            </td>
                                            <td className="px-6 py-4">
                                                <span className="text-on-surface-variant bg-surface-container-high/50 px-2.5 py-1 rounded-control">{loc.name}</span>
                                            </td>
                                            <td className="px-6 py-4 font-mono text-xs">
                                                {loc.size > 1024 ? `${(loc.size/1024).toFixed(1)} KB` : `${loc.size} Bytes`}
                                            </td>
                                            <td className="px-6 py-4 text-right">
                                                <div className="flex items-center justify-end gap-4 opacity-70 group-hover:opacity-100 -opacity whitespace-nowrap">
                                                    <button onClick={() => triggerDownload(loc.id)} className="text-on-surface-variant hover:text-success tooltip flex items-center justify-center" title={t('I18N_MGMT.DOWNLOAD_TOOLTIP')}>
                                                        <Download size={16} />
                                                    </button>
                                                    {!isCore && (
                                                        <button onClick={() => handleDelete(loc.id)} className="text-on-surface-variant hover:text-error tooltip flex items-center justify-center" title={t('I18N_MGMT.DELETE_TOOLTIP')}>
                                                            <Trash2 size={16} />
                                                        </button>
                                                    )}
                                                </div>
                                            </td>
                                        </tr>
                                    );
                                })
                            )}
                        </tbody>
                    </table>
                </div>
            </div>
        </div>
    );
};

export default I18nManagement;
