const fs = require('fs');

const file = 'd:/project/one-api/daof-ai-hub/i18n/zh-CN.json';
const zh = JSON.parse(fs.readFileSync(file, 'utf8'));

const mapping = {
  "阵列实体": "用户管理",
  "多态语境": "系统语言",
  "渠道枢纽": "渠道管理",
  "上帝管辖": "常规用户管理",
  "语境中心": "界面语言管理",
  "异动日志": "系统审计日志",
  "微观级流转监控": "代币流水审计",
  "全域令牌分发网": "API 调用令牌分发系统",
  "渠道矩阵": "渠道连接点",
  "最高执行官": "系统总管理员",
  "唯一执行代号": "全局登录用户名",
  "平民": "受约束用户",
  "高维权限覆写通道": "紧急管理员提权操作区",
  "实体连接器": "外部鉴权绑定",
  "身份信标": "系统唯一识别名",
  "矩阵接入时段": "注册建档日期",
  "跃迁中": "活跃与启用",
  "已冰封": "账户已锁停",
  "已全息封禁": "黑名单拦截",
  "容许算力定额": "API 免费可用额度",
  "防刷风控策略": "验证注册与拦截策略",
  "凭证通证": "授权私钥串",
  "发生时标": "审计入库时间",
  "调用凭证": "所使用的认证凭证",
  "跃迁模型": "所指派的大模型",
  "燃耗": "发生金额扣款",
  "底核覆写中": "资料覆盖提交中",
  "行使最高权力，篡改指令": "提交管理员参数变更",
  "全息写入中": "配置参数上传保存中",
  "矩阵烙印中": "账号注册建档中",
  "物理抹除根除": "永久强行注销实体",
  "记忆海数据调取中": "正从服务器拉取资料",
  "降维清理": "不可挽回地物理删除",
  "价格法典配置": "代理渠道资金费率设置",
  "该数字躯壳暂无历史风控操作记录": "暂未查阅到当前账号留存的操作历史。",
  "深空矩阵": "持久化存储数据库",
  "防沉迷风控阶梯机制": "高并发及巨额请求长文本倍率",
  "量子包解析异常": "传入参数格式非法致解析失败",
  "密保校验崩溃": "核准密码发生验证出错",
  "该数字档案已被永久封禁": "相关实体因违规已失去准入许可被封禁",
  "协议刷新成功，全站解除锁定": "配置重构刷新成功，安全屏障解除"
};

function traverseAndReplace(obj) {
  for (const key in obj) {
    if (typeof obj[key] === 'object' && obj[key] !== null) {
      traverseAndReplace(obj[key]);
    } else if (typeof obj[key] === 'string') {
      let val = obj[key];
      for (const [oldWord, newWord] of Object.entries(mapping)) {
        // use split+join for global replace of plain strings
        val = val.split(oldWord).join(newWord);
      }
      obj[key] = val;
    }
  }
}

traverseAndReplace(zh);
fs.writeFileSync(file, JSON.stringify(zh, null, 2) + '\n', 'utf8');
console.log('Sanitization applied to zh-CN.json');
