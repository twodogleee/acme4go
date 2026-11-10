/**
 * 管理端统一的 Naive UI 视觉主题覆盖。
 *
 * @author Andersen
 */
import type { GlobalThemeOverrides } from 'naive-ui'

/** acmeTheme 定义全局色彩、排版、圆角和核心组件样式。 */
export const acmeTheme: GlobalThemeOverrides = {
  common: {
    primaryColor: '#4d6859',
    primaryColorHover: '#40584b',
    primaryColorPressed: '#354b40',
    primaryColorSuppl: '#4d6859',
    infoColor: '#62758e',
    successColor: '#5f7c69',
    warningColor: '#b47747',
    errorColor: '#b45955',
    bodyColor: '#f7f5f1',
    cardColor: '#fffefa',
    modalColor: '#fffefa',
    popoverColor: '#fffefa',
    tableColor: '#fffefa',
    inputColor: '#fffefa',
    borderColor: '#e4dfd5',
    dividerColor: '#eeeae3',
    textColorBase: '#282923',
    textColor1: '#34362f',
    textColor2: '#66665d',
    textColor3: '#8b897f',
    placeholderColor: '#aaa69c',
    fontFamily: "Inter, ui-sans-serif, -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', sans-serif",
    fontFamilyMono: "'SFMono-Regular', Consolas, 'Liberation Mono', monospace",
    borderRadius: '10px',
    borderRadiusSmall: '8px',
  },
  Button: {
    borderRadiusTiny: '7px',
    borderRadiusSmall: '8px',
    borderRadiusMedium: '8px',
    borderRadiusLarge: '9px',
    fontWeight: '600',
    heightSmall: '32px',
    heightMedium: '40px',
    heightLarge: '44px',
    fontSizeSmall: '12px',
    fontSizeMedium: '13px',
    iconSizeSmall: '16px',
    iconSizeMedium: '17px',
    iconMarginSmall: '6px',
    iconMarginMedium: '7px',
    paddingSmall: '0 10px',
    paddingMedium: '0 16px',
  },
  Card: {
    borderRadius: '12px',
    titleFontSizeMedium: '15px',
    paddingMedium: '20px',
  },
  Input: {
    borderRadius: '8px',
    heightMedium: '40px',
  },
  Form: {
    labelFontWeight: '650',
    labelFontSizeTopMedium: '12px',
    labelHeightMedium: '24px',
    labelPaddingVertical: '0 0 8px',
    feedbackFontSizeMedium: '12px',
  },
  Select: {
    peers: {
      InternalSelection: {
        borderRadius: '8px',
        heightMedium: '40px',
      },
    },
  },
  DataTable: {
    borderColor: '#ece7df',
    thColor: '#faf8f4',
    thColorHover: '#faf8f4',
    tdColorHover: '#fbfaf7',
    thTextColor: '#77766d',
    tdTextColor: '#45473f',
    thFontWeight: '650',
  },
  Tag: {
    borderRadius: '999px',
  },
  Notification: {
    borderRadius: '12px',
  },
}
