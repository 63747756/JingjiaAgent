/* Editable PPTX. All inputs are local reviewed content, never executable code. */
const fs = require('node:fs');
const path = require('node:path');
const PptxGenJS = require('pptxgenjs');

async function build(spec, output) {
  if (!spec.title || !Array.isArray(spec.slides) || !spec.slides.length || spec.slides.length > 100) throw new Error('title and 1–100 slides required');
  const pptx = new PptxGenJS(); pptx.layout = 'LAYOUT_WIDE';
  pptx.author = spec.author || ''; pptx.subject = spec.title; pptx.title = spec.title; pptx.lang = 'zh-CN';
  pptx.theme = {headFontFace: 'Noto Sans CJK SC', bodyFontFace: 'Noto Sans CJK SC', lang: 'zh-CN'};
  spec.slides.forEach((item, index) => {
    if (typeof item.title !== 'string') throw new Error('slide title required');
    const slide = pptx.addSlide(); slide.background = {color:'F7F9FC'};
    slide.addText(item.title, {x:0.65,y:0.5,w:12,h:0.8,fontSize:27,bold:true,color:'16253F',breakLine:false,margin:0});
    const bullets = item.bullets || [];
    if (!Array.isArray(bullets) || bullets.length > 8 || bullets.some(text => typeof text !== 'string' || text.length > 180)) throw new Error('split dense content into more slides');
    if (bullets.length) slide.addText(bullets.map(text=>({text,options:{bullet:{indent:20},breakLine:true}})), {x:0.8,y:1.65,w:item.chart ? 5.5 : 11.7,h:4.8,fontSize:20,color:'24354E',paraSpaceAfterPt:15,margin:0.1,valign:'top'});
    if (item.chart) {
      const chart = item.chart;
      if (!Array.isArray(chart.labels) || !Array.isArray(chart.values) || chart.labels.length !== chart.values.length || chart.values.some(value=>typeof value!=='number' || !Number.isFinite(value))) throw new Error('chart labels and numeric values must align');
      slide.addChart(pptx.ChartType.bar,[{name:chart.series || '数据',labels:chart.labels,values:chart.values}],{x:6.8,y:1.7,w:5.6,h:4.5,catAxisLabelFontFace:'Noto Sans CJK SC',showLegend:false,showValue:true,showTitle:!!chart.title,title:chart.title || '',chartColors:['4460D8'],showCatName:false,showBorder:false});
    }
    slide.addText(item.source ? '来源：'+item.source : '', {x:0.7,y:6.93,w:11.3,h:0.3,fontSize:10,color:'667085',margin:0});
    slide.addText(String(index+1),{x:12.2,y:6.93,w:0.4,h:0.3,fontSize:10,color:'667085',margin:0,align:'right'});
  });
  fs.mkdirSync(path.dirname(output),{recursive:true}); await pptx.writeFile({fileName:output});
}
if (require.main === module) {
  if (process.argv.length !== 4) throw new Error('usage: build_deck.cjs input.json output.pptx');
  build(JSON.parse(fs.readFileSync(process.argv[2],'utf8')),process.argv[3]).catch(error=>{process.stderr.write(error.message+'\n');process.exitCode=1;});
}
module.exports = {build};
