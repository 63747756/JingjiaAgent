import ace from "ace-builds/src-noconflict/ace";

const cssText = `

.ace-jingjiaagent .ace_gutter {
  background: #f8f8f8;
  color: #2e3440
}

.ace-jingjiaagent {
  background-color: #FFFFFF;
  color: #2e3440;
  line-height: 1.8 !important;
}

.ace-jingjiaagent .ace_cursor {
  color: #AEAFAD
}

.ace-jingjiaagent .ace_marker-layer .ace_selection {
  background: #e0e0e0
}

.ace-jingjiaagent.ace_multiselect .ace_selection.ace_start {
  box-shadow: 0 0 3px 0px #FFFFFF;
}

.ace-jingjiaagent .ace_marker-layer .ace_step {
  background: rgb(255, 255, 0)
}

.ace-jingjiaagent .ace_marker-layer .ace_bracket {
  margin: -1px 0 0 -1px;
  border: 1px solid #D1D1D1
}

.ace-jingjiaagent .ace_marker-layer .ace_active-line {
  background: #f4f4f4
}

.ace-jingjiaagent .ace_gutter-active-line {
  background-color : #f4f4f4
}

.ace-jingjiaagent .ace_marker-layer .ace_selected-word {
  border: 1px solid #e8e8e8
}

.ace-jingjiaagent .ace_invisible {
  color: #D1D1D1
}

.ace-jingjiaagent .ace_keyword,
.ace-jingjiaagent .ace_meta,
.ace-jingjiaagent .ace_storage,
.ace-jingjiaagent .ace_storage.ace_type,
.ace-jingjiaagent .ace_support.ace_type {
  color: #8959A8
}

.ace-jingjiaagent .ace_keyword.ace_operator {
  color: #3E999F
}

.ace-jingjiaagent .ace_constant.ace_character,
.ace-jingjiaagent .ace_constant.ace_language,
.ace-jingjiaagent .ace_constant.ace_numeric,
.ace-jingjiaagent .ace_keyword.ace_other.ace_unit,
.ace-jingjiaagent .ace_support.ace_constant,
.ace-jingjiaagent .ace_variable.ace_parameter {
  color: #F5871F
}

.ace-jingjiaagent .ace_constant.ace_other {
  color: #666969
}

.ace-jingjiaagent .ace_invalid {
  color: #FFFFFF;
  background-color: #C82829
}

.ace-jingjiaagent .ace_invalid.ace_deprecated {
  color: #FFFFFF;
  background-color: #8959A8
}

.ace-jingjiaagent .ace_fold {
  background-color: #4271AE;
  border-color: #2e3440
}

.ace-jingjiaagent .ace_entity.ace_name.ace_function,
.ace-jingjiaagent .ace_support.ace_function,
.ace-jingjiaagent .ace_variable {
  color: #C99E00
}

.ace-jingjiaagent .ace_support.ace_class,
.ace-jingjiaagent .ace_support.ace_type {
  color: #C99E00
}

.ace-jingjiaagent .ace_string {
  color: #5e81ac;
}

.ace-jingjiaagent .ace_markup {
  color: #8fbcbb !important;
}

.ace-jingjiaagent .ace_heading {
  color: #5e81ac;
  font-weight: bold;
}

.ace-jingjiaagent .ace_comment {
  color: #8E908C;
}

.dark .ace-jingjiaagent {
  background-color: #0d1117;
  color: #c9d1d9;
}

.dark .ace-jingjiaagent .ace_gutter {
  background: #161b22;
  color: #8b949e;
}

.dark .ace-jingjiaagent .ace_cursor {
  color: #c9d1d9;
}

.dark .ace-jingjiaagent .ace_marker-layer .ace_selection {
  background: #264f78;
}

.dark .ace-jingjiaagent.ace_multiselect .ace_selection.ace_start {
  box-shadow: 0 0 3px 0 #0d1117;
}

.dark .ace-jingjiaagent .ace_marker-layer .ace_step {
  background: #4b3f16;
}

.dark .ace-jingjiaagent .ace_marker-layer .ace_bracket {
  border-color: #6e7681;
}

.dark .ace-jingjiaagent .ace_marker-layer .ace_active-line,
.dark .ace-jingjiaagent .ace_gutter-active-line {
  background: #161b22;
}

.dark .ace-jingjiaagent .ace_marker-layer .ace_selected-word {
  border-color: #6e7681;
}

.dark .ace-jingjiaagent .ace_invisible {
  color: #484f58;
}

.dark .ace-jingjiaagent .ace_keyword,
.dark .ace-jingjiaagent .ace_meta,
.dark .ace-jingjiaagent .ace_storage,
.dark .ace-jingjiaagent .ace_storage.ace_type,
.dark .ace-jingjiaagent .ace_support.ace_type {
  color: #ff7b72;
}

.dark .ace-jingjiaagent .ace_keyword.ace_operator {
  color: #79c0ff;
}

.dark .ace-jingjiaagent .ace_constant.ace_character,
.dark .ace-jingjiaagent .ace_constant.ace_language,
.dark .ace-jingjiaagent .ace_constant.ace_numeric,
.dark .ace-jingjiaagent .ace_keyword.ace_other.ace_unit,
.dark .ace-jingjiaagent .ace_support.ace_constant,
.dark .ace-jingjiaagent .ace_variable.ace_parameter {
  color: #79c0ff;
}

.dark .ace-jingjiaagent .ace_constant.ace_other {
  color: #a5d6ff;
}

.dark .ace-jingjiaagent .ace_invalid {
  color: #ffdcd7;
  background-color: #da3633;
}

.dark .ace-jingjiaagent .ace_invalid.ace_deprecated {
  color: #ffdcd7;
  background-color: #8957e5;
}

.dark .ace-jingjiaagent .ace_fold {
  background-color: #58a6ff;
  border-color: #c9d1d9;
}

.dark .ace-jingjiaagent .ace_entity.ace_name.ace_function,
.dark .ace-jingjiaagent .ace_support.ace_function,
.dark .ace-jingjiaagent .ace_variable,
.dark .ace-jingjiaagent .ace_support.ace_class,
.dark .ace-jingjiaagent .ace_support.ace_type {
  color: #d2a8ff;
}

.dark .ace-jingjiaagent .ace_string,
.dark .ace-jingjiaagent .ace_heading {
  color: #a5d6ff;
}

.dark .ace-jingjiaagent .ace_markup {
  color: #7ee787 !important;
}

.dark .ace-jingjiaagent .ace_comment {
  color: #8b949e;
}
`;



ace.define(
    "ace/theme/jingjiaagent",
    ["require", "exports", "module", "ace/lib/dom"],
    function (require: any, exports: any) {
      exports.isDark = true;
      exports.cssClass = "ace-jingjiaagent";
      exports.cssText = cssText;
  
      const dom = require("ace/lib/dom");
      dom.importCssString(cssText, exports.cssClass);
    }
  );
