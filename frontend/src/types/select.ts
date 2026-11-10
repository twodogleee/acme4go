/** 结构化单选项的展示契约，不包含服务请求或业务权限判断。 @author Andersen */
export interface SelectChoice {
  value: string | number // 原始业务标识，保持数字和字符串类型。
  label: string // 收起选择框时显示的简短名称。
  description?: string // 选项的用途或主要区别。
  details?: string[] // 分行展示的资源元数据。
  badge?: string // 简短的状态或类别标记。
  disabled?: boolean // 调用方决定该选项是否可被选择。
}
