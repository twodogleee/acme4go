/** 只读详情的展示契约，页面显式挑选字段并负责脱敏与业务文案。 @author Andersen */
export interface DetailField {
  label: string // 字段的可读名称，在所属分组内唯一。
  value: string | number | string[] | null | undefined // 数组逐项展示；保留数字零，空值使用占位。
  group?: string // 业务分组名称，按照首次出现的顺序展示。
  wide?: boolean // 域名集合、路径及结果说明等长内容占据整行。
  mono?: boolean // 标识、指纹等技术值采用等宽字体。
  status?: string // 非空时使用统一状态徽标，value 提供业务显示文案。
  error?: boolean // 已知失败原因以语义颜色强调，仍完整保留文本。
}
