const fs = require('fs');
const path = require('path');

const replacements = {
  "量子包解析失败": "请求参数解析失败",
  "拉取数据矩阵失败": "获取数据失败",
  "名字是生存的基石，不可为空": "用户名不能为空",
  "代号冲突！该昵称已被铸造": "抱歉，该用户名已被占用",
  "在根矩阵实例化账号失败": "用户创建失败",
  "用户装载完成！": "用户创建成功",
  "解析异常": "数据解析异常",
  "该节点已消失": "未找到相关记录",
  "防自杀模块已介入：不能封印最后的执剑人！": "操作遭拒：无法封禁唯一的系统管理员",
  "更新失败，目标可能有冲突": "数据更新失败，存在冲突",
  "更新完毕且全节点同步生效！": "更新操作已成功保存",
  "缺乏通行凭证": "缺少身份认证凭证",
  "凭证格式损毁": "身份凭证格式无效",
  "找不到该档案记录": "未能检索到用户档案",
  "此标识早已湮灭": "未找到相关记录或已被删除",
  "防自杀模块已介入：不可抹绝最后一位守护者！": "操作拦截：不可删除系统唯一的管理员",
  "神罚执行：数据已被物理根除": "数据已彻底删除",
  "衍生凭证配发完成": "API 凭证创建成功",
  "底层记录熔铸失败": "数据库记录创建失败",
  "已达到令牌并发上限": "该账户关联令牌配额已满",
  "数据异常": "传入参数格式出现异常",
  "令牌实体丢失": "未能找到对应令牌凭证",
  "状态锁定": "状态已修改保存",
  "实体不在你的权限域下": "访问遭拒，此资源不在您的管辖权限内",
  "通道已断流，凭证焚毁完毕": "相关令牌已被彻底删除",
  "读取审计日记失败": "获取用户审计信息失败",
  "无法读取语言包目录": "系统日志：获取多语言配置文件目录失败",
  "非法语言包标识": "无效的多语言标识符",
  "JSON 矩阵反序列化失败，格式可能已损坏": "请求的 JSON 载荷解析失败，格式可能无效",
  "语言包烙印硬盘失败": "配置包写入服务器本地文件系统失败",
  "语言包注入完成": "多语言配置文件上传完毕",
  "根基语言包 \\(zh-CN / en-US\\) 受到底层逻辑保护，不可抹除": "系统核心内建语言包不可删除",
  "语言包湮灭失败或不存在": "删除执行过程中报错，可能该文件不存在",
  "语言包已彻底从硬盘抹去": "多语言设定资源已清理删除完成",
  "无效的代码负载": "授权码 (OAuth Code) 验证失败或无效",
  "管理员尚未初始化 GitHub 鉴权服务": "后台尚未完成该第三方平台 OAuth 环境的接入项设置",
  "无法连接到 GitHub 认证节点": "出站网络异常，无法连接第三方 API",
  "GitHub 授权验证码已过期或无效": "第三方颁发的客户端授权码已过期失效",
  "无法获取 GitHub 档案信息": "请求第三方用户公开身份信息错误",
  "获取 GitHub 档案异常": "获取档案过程中引发无法恢复的异常",
  "系统启动沙盒安全策略，已锁定您的 GitHub 凭据，请补全短信身份验证以破拆沙盒。": "安全校验未完成：受新账号安全策略影响，请先验证手机号码以完成注册核验。",
  "检测到安全凭据，请设立您的专属系统内绰号": "联合登录完成，请指定本平台内用户名用作唯一标识",
  "系统核心尚未激活\\(System Not Ready\\)": "系统尚未完全初始化配置服务向导（System Not Setup）",
  "Protocol Violation. Local connection required.": "系统拦截判定：连接非法，接口强制要求本地域内环境调用",
  "物理层结界阻断：系统检测到您的物理星域处于外网，已屏蔽暗网通道入口。": "安全机制阻断：由于侦测到您的 IP 来源并非内网环境被隔离，这通常意味着该功能未向外网放开或被墙外置反代",
  "缺乏最高指挥权令牌": "管理后台入口阻断：请求表单严重缺少对应鉴权 Token",
  "令牌格式损毁": "系统警告提醒：发来的管理权令牌串不符合规定形态",
  "伪造指令，拒绝越权访问": "未授权拒绝处理：您的管理 Token 与该后端资源所需的系统级权限不契合",
  "配置已通过最强 AES-GCM 封装并锁定入库，且全局映射缓存已重启": "底层系统配置更新任务已提交至全内存同步更新"
};

function processDir(dir) {
    const files = fs.readdirSync(dir);
    for (const file of files) {
        const fullPath = path.join(dir, file);
        const stat = fs.statSync(fullPath);
        if (stat.isDirectory()) {
            processDir(fullPath);
        } else if (fullPath.endsWith('.go')) {
            let content = fs.readFileSync(fullPath, 'utf8');
            let modified = false;

            for (const [key, value] of Object.entries(replacements)) {
                // To replace keys globally
                const regex = new RegExp(key, 'g');
                if (regex.test(content)) {
                    content = content.replace(regex, value);
                    modified = true;
                }
            }

            if (modified) {
                fs.writeFileSync(fullPath, content, 'utf8');
                console.log(`Updated: ${fullPath}`);
            }
        }
    }
}

const targetDirs = [
    path.join(__dirname, 'controller'),
    path.join(__dirname, 'middleware')
];

targetDirs.forEach(dir => {
    if (fs.existsSync(dir)) processDir(dir);
});

console.log('Sanitization complete.');
