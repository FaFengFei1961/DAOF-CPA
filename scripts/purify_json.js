const fs = require('fs');

const path = 'd:/project/one-api/daof-ai-hub/i18n/zh-CN.json';
let raw = fs.readFileSync(path, 'utf8');
let json = JSON.parse(raw);

// A map of simple substring replacements across ALL string values
const replacements = {
    '初始化至数字网络': '初始化账号',
    '将用户 [{{target}}] 的余额，': '将用户 [{{target}}] 的余额',
    '物理注销': '彻底删除',
    '物理抹除': '删除数据',
    '物理层结界阻断：系统检测到您的物理星域处于外网，已屏蔽暗网通道入口。': '拒绝访问：管理接口仅限同网段与本地设备访问 (LanGuard)。',
    '系统核心尚未激活': '系统尚未完成初始化配置。',
    '不可抹绝最后一位守护者！': '不能删除系统中最后一位管理员！',
    '不能封印最后的执剑人！': '不能封禁系统中最后一位管理员！',
    '防自杀模块已介入：': '系统保护提示：',
    '该底层模型 ID 已经盘踞在网关中': '该模型 ID 已经存在。',
    '目标模型已失踪': '找不到目标模型。',
    '模型规则已被完全剥离': '模型已成功删除。',
    '构建新隧道线路宣告失败': '创建新渠道失败。',
    '上游源节点丢失信号': '找不到该渠道。',
    '无法解密基础渠道配置': '获取渠道配置失败。',
    '无法更新渠道连接点路由': '更新渠道配置失败。',
    '基础渠道销毁失败': '删除渠道失败。',
    '目标绑定矩阵配置已消失': '未找到对应的渠道。',
    '无法即时重构价格逻辑': '模型定价更新失败。',
    '无法将模型参数熔合此通道': '模型添加失败。',
    '禁止抹除系统核心底座语种': '系统内置语言包不可删除。',
    '密文长度小于安全游标标准': '密码长度不符合安全标准。',
    '该指令源不存在': '目标接口不存在。',
    '核准密码发生验证出错，入口封闭': '鉴权失败，密码错误。',
    '新指令集存在违规漏洞': '提交的请求参数不合法。',
    '底层权限库覆写失败，可能与其他神名发生冲突。': '管理员信息保存失败，可能是账号名称冲突。',
    '语言包烙印硬盘失败': '保存语言包失败。',
    '语言包注入完成': '语言包上传成功。',
    '语言包湮灭失败或不存在': '删除语言包失败。',
    '语言包已彻底从硬盘抹去': '语言包已删除。',
    '实名核验完成，沙盒限制已解除': '身份验证通过，账号已恢复正常权限。',
    '名字烙印完成！': '系统展示名设置成功！',
    '全息校验中...': '验证中...',
    '账号注册建档中...': '账号创建中...',
    '向 AI 引擎全息管道 DAOF-CPA 发起鉴权，代表您同意服务条款': '登录或注册即代表您同意我们的服务条款',
    '更新完毕且全节点同步生效！': '配置保存成功！',
    '全息管理档案重构成功！注意：因为名称变动，您现在将会被注销，请使用新代号重新通过安全闸门进行认证！': '管理员更名成功。您的本次登录已经失效，请使用新后台地址重新登录！',
    '获取 GitHub 档案异常': '拉取 GitHub 资料失败',
    '系统防刷判定：此 Github 账号已经注册过': '该 GitHub 账号已经被绑定。',
    '创建根记录失败': '创建管理员账号失败。',
    '在根矩阵实例化账号失败': '创建账号失败。',
    '底层记录熔铸失败': '数据入库失败。',
    '衍生凭证配发完成': '生成 API 令牌成功。',
    '令牌实体丢失': '找不到该令牌。',
    '通道已断流，凭证焚毁完毕': 'API 令牌已删除。',
    '拉取数据矩阵失败': '获取数据列表失败。',
    '名字是生存的基石，不可为空': '名称字段不能为空。',
    '代号冲突！该昵称已被铸造': '该名称已经被占用，请换一个。',
    '该节点已消失': '找不到该用户档案。',
    '缺乏最高指挥权令牌': '权限不足：需要管理员身份。',
    '伪造指令，拒绝越权访问': '请求被拒绝或状态异常。',
    '通信异常': '网络连接中断',
    '全息覆写中...': '保存中...',
    '确认并锁定中枢': '确认并保存配置',
    '接入大权': '登录后台管理',
    '加载档案中...': '拉取用户资产中...',
    '检索生命体征中...': '加载配置中...',
    '个人数字档案': '个人中心',
    '紧急管理员提权操作区': '管理员后台账号重置',
    '此处可直接干涉系统最底层的最高指挥官档案。请谨慎操作：更改代号名称将瞬间导致您的专属管理后台入口 URL 发生变异！': '您可以在此更改超级管理员访问账号与密码。注意：更改管理员账号（即：新代号）后，您的默认管理入口也随之改变。',
    '将 [{{target}}] 初始化至数字网络。初始额度: [${{quota}}]': '创建了新用户 [{{target}}]，并分配初始额度 [${{quota}}]。',
    '铸造中...': '创建中...',
    '同步矩阵中...': '同步数据中...',
    '生成新指令域': '生成新的访问令牌',
    '解析深空日志中...': '加载日志中...',
    '底层深空通道尚未监测到任何活动日志。': '暂未查阅到任何流水日志。',
    '管理上游接入源点并在每个渠道内部进行绝对独立的高参定价阶梯设定': '管理不同的上游并发供应商及其代理接口，您可以在各通道里独立设定特定大模型的价格体系和权重。',
    '摧毁当前这根暗网数据管以及内部的所有细化定价策略，您确定？': '确认要删除此供货渠道以及挂载在上面的所有模型配置吗？',
    '非法的模型参数报文': '提交的请求数据有误',
    // UI Keys
    '管理矩阵网络内的所有接入节点。识别、干涉并在高危时彻底截断恶意使用者的模型流经。': '管理本站所有已注册的用户账号和API额度。',
    '强制签发新通行证': '添加新用户',
    '篡改账户档案': '修改账户信息',
    '确认行使职权': '确认执行',
    '搜查时间流...': '检索数据库中...',
    '暂未查阅到当前账号留存的操作历史。。': '当前用户的操作审计日记为空。',
    '永久强行注销实体': '物理删除',
    '强制创立档案': '管理员创建账号',
    '物理抹除': '执行删除',
    '篡改参数': '编辑信息',
    '网络阻断，核心引擎未响应': '网络不稳定或服务端已掉线。',
    '首次入驻安全约束': '首次部署初始化',
    '为保障全站物理安全，您必须立刻修改您的指令核载（账号）与高熵核准码（密码）。此后，`?sys=` 将永远绑定该新指令轴心思。': '欢迎使用。作为最后一步防线，请设置属于您自己的后台管理员通行证。今后将使用此账号登陆。',
    '避免使用 root, admin 等易猜词汇': '建议采用不常见的标识符号组合',
    '设定高熵核准码 (新密码)': '管理员专属密码',
    '设定新的指令轴心 (新账号)': '管理员专属账号',
    '核准码签发验证': '请输入密码',
    '由于目前底层串流代理引擎还在部署，暂时不会产生耗能流水日志。': '在此实时监控并审计所有的访问和令牌使用记录。',
    '配置已通过最强 AES-GCM 封装并锁定入库，且全局映射缓存已重启': '全局系统配置修改成功且已热复位。',
    '神级权限核准通过。': '管理员权限校验成功。',
    '配置重构刷新成功，安全屏障解除。': '初始化流程完毕，您可以开始进入面板！'
};

function processObj(obj) {
    if (typeof obj === 'string') {
        let str = obj;
        
        // Exact replace first
        for (let k in replacements) {
            str = str.split(k).join(replacements[k]);
        }
        
        // Generic word replacements
        str = str.replace(/指令核载/g, '管理员账号');
        str = str.replace(/高熵核准码/g, '管理密码');
        str = str.replace(/指令轴心思/g, '管理员账号');
        str = str.replace(/指令轴心/g, '管理员账号');
        str = str.replace(/指令源/g, 'API接口');
        str = str.replace(/指令枢纽/g, '操作');
        str = str.replace(/指令集/g, '配置请求');
        str = str.replace(/最高指挥官/g, '系统超管');
        str = str.replace(/执剑人/g, '管理员');
        str = str.replace(/守护者/g, '管理员');
        str = str.replace(/暗网数据管/g, '中转渠道连接');
        str = str.replace(/暗网通道入口/g, '管理员隧道入口');
        str = str.replace(/中枢/g, '配置');
        str = str.replace(/结界/g, '防线');
        str = str.replace(/深空日志/g, '调用日志');
        str = str.replace(/深空通道/g, '代理层');
        str = str.replace(/数字档案/g, '个人设置');
        str = str.replace(/档案信息/g, '账户信息');
        str = str.replace(/权限域/g, '权限范围');
        str = str.replace(/实体不在你的权限域下/g, '你没有该用户的管理权限');
        str = str.replace(/强行注销实体/g, '注销该账号');
        str = str.replace(/实体/g, '账号/渠道');
        str = str.replace(/生命体征/g, '配置状态');
        str = str.replace(/大权/g, '系统后台');
        str = str.replace(/熔铸/g, '保存');
        str = str.replace(/熔合/g, '添加');
        str = str.replace(/烙印硬盘/g, '写入磁盘');
        str = str.replace(/锻造/g, '配置');
        str = str.replace(/焚毁/g, '注销/删除');
        str = str.replace(/湮灭/g, '删除');
        str = str.replace(/抹绝/g, '移除');
        str = str.replace(/语境中心/g, '多语言管理');
        str = str.replace(/语境管理库/g, '界面语言管理');
        str = str.replace(/语境切片/g, '语言包');
        str = str.replace(/语境主键/g, '语言代号');
        str = str.replace(/字典拓扑大小/g, '文件体积');
        str = str.replace(/黑盒/g, '系统');
        str = str.replace(/漏洞/g, '问题');
        str = str.replace(/防爆盾/g, '安全验证');
        str = str.replace(/防刷风控与通行策略引擎/g, '注册风控策略配置');
        str = str.replace(/极致体感/g, '宽松模式');
        str = str.replace(/沙盒智控/g, '动态风控');
        str = str.replace(/终极提纯/g, '严格模式');
        str = str.replace(/大规模羊毛党/g, '恶意批量注册');
        str = str.replace(/全息管道/g, '代理服务');
        str = str.replace(/全息/g, '');
        str = str.replace(/深层校验/g, '短信手机校验');
        str = str.replace(/新密保/g, '新密码');
        str = str.replace(/新代号/g, '新账号');
        str = str.replace(/代号名称/g, '登录账号名');
        str = str.replace(/代号冲突/g, '账号名冲突');
        str = str.replace(/源点/g, '提供商');
        str = str.replace(/矩阵网络/g, '全局系统');
        str = str.replace(/底层模型 ID 已经盘踞在网关中/g, '渠道对应的模型 ID 已存在');
        
        return str;
    } else if (Array.isArray(obj)) {
        return obj.map(item => processObj(item));
    } else if (typeof obj === 'object' && obj !== null) {
        let newObj = {};
        for (let k in obj) {
            newObj[k] = processObj(obj[k]);
        }
        return newObj;
    }
    return obj;
}

const cleanedJson = processObj(json);

// specific spot fixes
cleanedJson.MENU.DASHBOARD = "面板总览";
cleanedJson.APP.INITIALIZING = "部署系统资源中...";
cleanedJson.APP.BANNED.ACCEPT_BTN = "确认并登出";
cleanedJson.TOKEN_MGMT.LOGS_DESC = "由于目前底层串流代理引擎仍处于灰度切换期，这边的代理调用流水日志将会短暂地延迟反馈入库。";
cleanedJson.TOKEN_MGMT.LOGS_EMPTY = "暂无系统令牌审计日志记录产生。";
cleanedJson.TOKEN_MGMT.TABLE_HEAD_CTRL = "操作项";
cleanedJson.TOKEN_MGMT.CREATE_CARD_TITLE = "管理访问令牌";
cleanedJson.TOKEN_MGMT.CREATE_CARD_DESC = "分配不同的 API Key 用于独立统计不同应用的配额使用量。";
cleanedJson.ADMIN_LOGIN.SETUP_DESC = "作为DAOF-CPA系统的所有者及超级管理员，您必须配置您的后台专有账户密码。配置生效后，公共界面将隐藏您的入口。";
cleanedJson.AUTH.FOOTER_TOS = "使用 DAOF-CPA 服务即表示您受限于并同意我们的隐私政策和用户服务条款以及最终核算标准";

fs.writeFileSync(path, JSON.stringify(cleanedJson, null, 2), 'utf8');
console.log("JSON Translation Purified Successfully!");
