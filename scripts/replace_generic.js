const fs = require('fs');
const path = require('path');

const replacements = {
  "安全机制阻断：由于侦测到您的 IP 来源并非内网环境被隔离，这通常意味着该功能未向外网放开或被墙外置反代": "访问被拒绝(403 Forbidden)：环境异常或权限不足",
  "系统拦截判定：连接非法，接口强制要求本地域内环境调用": "被拒绝(403)：访问该资源受限",
  "底层系统配置更新任务已提交至全内存同步更新": "配置重载成功",
  "数据库记录创建失败": "系统处理内部异常，请稍后重试",
  "管理后台入口阻断：请求表单严重缺少对应鉴权 Token": "未识别到授权凭证，操作终止",
  "系统警告提醒：发来的管理权令牌串不符合规定形态": "非法或受损的格式请求",
  "未授权拒绝处理：您的管理 Token 与该后端资源所需的系统级权限不契合": "越权访问行为已被阻止",
  "缺少身份认证凭证": "访问被拒绝：未授权",
  "身份凭证格式无效": "请求已拦截：非法的凭证载荷",
  "访问遭拒，此资源不在您的管辖权限内": "权限受限（403 Forbidden）",
  "系统日志：获取多语言配置文件目录失败": "系统配置载入异常，请联系管理员",
  "配置包写入服务器本地文件系统失败": "系统安全保护：文件写入操作失败",
  "后台尚未完成该第三方平台 OAuth 环境的接入项设置": "暂时无法提供该授权模式，请使用其他方式登录",
  "出站网络异常，无法连接第三方 API": "第三方服务响应超时(502)",
  "请求第三方用户公开身份信息错误": "无法同步上游服务器资料",
  "获取档案过程中引发无法恢复的异常": "第三方接口同步异常",
  "系统尚未完全初始化配置服务向导（System Not Setup）": "服务未就绪(503 Service Unavailable)",
  "请求的 JSON 载荷解析失败，格式可能无效": "无效的数据结构或格式损坏"
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
                // escape regex special chars
                const escapedKey = key.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
                const regex = new RegExp(escapedKey, 'g');
                if (regex.test(content)) {
                    content = content.replace(regex, value);
                    modified = true;
                }
            }

            if (modified) {
                fs.writeFileSync(fullPath, content, 'utf8');
                console.log(`Updated generics: ${fullPath}`);
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

console.log('Generic sanitization complete.');
