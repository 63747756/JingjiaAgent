// Read only the selection staged by the business adapter. Signed source URLs
// never become prompts, CLI settings, or model-visible messages.
import { createHash } from "node:crypto";
import { existsSync, lstatSync, mkdirSync, readFileSync, readlinkSync, realpathSync, renameSync, symlinkSync, unlinkSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import type { RunnerOptions } from "./types.js";

function regular(path:string, limit:number):Buffer {
  if(realpathSync(path)!==resolve(path) || !lstatSync(path).isFile() || lstatSync(path).size>limit) throw new Error("Native asset path rejected");
  return readFileSync(path);
}
function json(path:string):any { return JSON.parse(regular(path,1<<20).toString("utf8")); }
function directory(path:string):void {
  mkdirSync(path,{recursive:true,mode:0o700});
  if(realpathSync(path)!==resolve(path) || !lstatSync(path).isDirectory()) throw new Error("Native asset directory rejected");
}
function atomic(path:string, value:unknown):void {
  const temporary=path+"."+process.pid+".tmp";
  writeFileSync(temporary,JSON.stringify(value),{flag:"wx",mode:0o600});
  try { renameSync(temporary,path); } finally { if(existsSync(temporary))unlinkSync(temporary); }
}

export function nativeAssets(options:RunnerOptions,provider:"codex"|"claude"):RunnerOptions {
  const task=process.env.JINGJIAAGENT_TASK_ID||"";
  if(!/^[a-f0-9-]{36}$/.test(task)) throw new Error("Native asset task correlation unavailable");
  const root=join(options.home,".codingmatrix/project-tpl/.ai-ready");
  const rules=join("/data/state/jingjiaagent-native",task+".rules.json");
  let context=options.systemContext;
  if(existsSync(rules)) {
    const selected=json(rules);
    if(selected.task_id!==task || !Array.isArray(selected.rules) || selected.rules.length>128)throw new Error("Native rule selection rejected");
    let bytes=0;
    for(const path of selected.rules) {
      if(typeof path!=="string" || dirname(path)!==join(root,"rules"))throw new Error("Native rule selection rejected");
      const content=regular(path,1<<20);bytes+=content.length;
      if(bytes>4<<20)throw new Error("Native rule selection exceeds limit");
      context+="\n\n"+content.toString("utf8");
    }
  }
  const resourceManifest=join(root,".runtime-resources.json");
  const selected=existsSync(resourceManifest)?json(resourceManifest).resources?.skills||[]:[];
  if(!Array.isArray(selected) || selected.length>128)throw new Error("Native skill selection rejected");
  const skills:Record<string,string>={};
  for(const item of selected) {
    if(typeof item.name!=="string" || !item.name || /[\\/\x00-\x1f]/.test(item.name) || [".",".."].includes(item.name))throw new Error("Native skill name rejected");
    const source=join(root,"skills",item.name);
    regular(join(source,"SKILL.md"),1<<20);
    skills[item.name]=source;
  }
  const target=join(options.home,provider==="claude"?".claude/skills":".agents/skills");
  const state=join("/data/state/jingjiaagent-native",provider+"-"+task+".skills.json");
  directory(dirname(state));directory(target);
  const previous:Record<string,string>=existsSync(state)?json(state):{};
  // Never replace an employee-created directory or an unrelated symlink.
  for(const [name,source] of Object.entries({...previous,...skills})) {
    if(dirname(join(target,name))!==target || dirname(source)!==join(root,"skills"))throw new Error("Native skill receipt rejected");
    const link=join(target,name);
    let info;
    try { info=lstatSync(link); } catch(error:any) { if(error.code!=="ENOENT")throw error; }
    if(info && (!info.isSymbolicLink() || readlinkSync(link)!==(previous[name]||source)))throw new Error("Native skill path already owned");
  }
  for(const name of Object.keys(previous))if(!skills[name]) { const link=join(target,name);try { unlinkSync(link); } catch(error:any) { if(error.code!=="ENOENT")throw error; } }
  for(const [name,source] of Object.entries(skills))if(!existsSync(join(target,name)))symlinkSync(source,join(target,name),"dir");
  atomic(state,skills);
  return {...options,systemContext:context,skills:Object.keys(skills)};
}

export function nativeAttachments(prompt:string):{text:string;files:{path:string;mime:string;bytes:Buffer}[]} {
  const task=process.env.JINGJIAAGENT_TASK_ID||"";
  if(!/^[a-f0-9-]{36}$/.test(task))throw new Error("Native attachment task correlation unavailable");
  const pointer=join("/data/state/jingjiaagent-attachments",task+".json");
  if(!existsSync(pointer))return {text:prompt,files:[]};
  const selected=json(pointer);
  if(selected.task_id!==task || !/^[a-f0-9-]{36}$/.test(selected.command_id) || !Array.isArray(selected.files) || selected.files.length>10)throw new Error("Native attachment selection rejected");
  const root=join("/workspace/.jingjiaagent/attachments",task,selected.command_id);
  const files=[];
  for(const item of selected.files) {
    if(typeof item.path!=="string" || dirname(item.path)!==root)throw new Error("Native attachment path rejected");
    const bytes=regular(item.path,32<<20);
    if(bytes.length!==item.size || createHash("sha256").update(bytes).digest("hex")!==item.sha256)throw new Error("Native attachment content changed");
    prompt+="\n用户附件 "+item.filename+"，本地路径："+item.path;
    files.push({path:item.path,mime:String(item.mime),bytes});
  }
  return {text:prompt,files};
}

export function claudePrompt(text:string):string|AsyncIterable<any> {
  const attached=nativeAttachments(text);
  const images=attached.files.filter(item=>["image/png","image/jpeg","image/gif","image/webp"].includes(item.mime));
  if(!images.length)return attached.text;
  return (async function*(){yield {type:"user",message:{role:"user",content:[{type:"text",text:attached.text},...images.map(item=>({type:"image",source:{type:"base64",media_type:item.mime,data:item.bytes.toString("base64")}}))]},parent_tool_use_id:null,session_id:""};})();
}
