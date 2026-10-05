import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { lstatSync, readFileSync, realpathSync, writeFileSync, renameSync, rmSync } from "node:fs";
import { join, resolve } from "node:path";
import { homedir } from "node:os";
import { randomUUID } from "node:crypto";
import type { AgentEvent } from "./agent-event.js";

// Platform CLI settings point at the product model proxy. The daemon's facade
// replaces conventional API-key variables. Restore the matching per-command
// configuration staged by the adapter, never the Sandbox's creation-time env.
export function nativeModelEnvironment(provider:string, source:NodeJS.ProcessEnv, configRoot="/data/state/monkeycode-native"):NodeJS.ProcessEnv {
  const env={...source};
  if(!env.AGENT_COMPOSE_RUN_ID || !env.MONKEYCODE_TASK_ID) return env;
  const task=env.MONKEYCODE_TASK_ID;
  if(!/^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/.test(task)) throw new Error("Native platform model task correlation unavailable");
  const path=join(configRoot,task+".model.json");
  let selected:any;
  try {
    const stat=lstatSync(path);
    if(!stat.isFile() || stat.size>1<<20 || realpathSync(path)!==resolve(path)) throw new Error("invalid model file");
    selected=JSON.parse(readFileSync(path,"utf8"));
  } catch { throw new Error("Native platform model configuration unavailable"); }
  if(selected?.task_id!==task || ![selected?.api_key,selected?.base_url,selected?.model].every(value=>typeof value==="string" && value.trim())) {
    throw new Error("Native platform model configuration is incomplete");
  }
  const key=selected.api_key,base=selected.base_url,model=selected.model;
  // Do not pass the obsolete model credentials on to native child processes.
  delete env.MONKEYCODE_MODEL_API_KEY;
  delete env.MONKEYCODE_MODEL_BASE_URL;
  delete env.MONKEYCODE_MODEL_NAME;
  if(provider==="claude") {
    env.ANTHROPIC_API_KEY=key;env.ANTHROPIC_AUTH_TOKEN=key;env.ANTHROPIC_BASE_URL=base;env.ANTHROPIC_MODEL=model;
  } else {
    env.OPENAI_API_KEY=key;env.OPENAI_BASE_URL=base;env.OPENAI_MODEL=model;
  }
  return env;
}

// Claude's user settings env overrides child-process env. Refresh only the
// platform's model fields before starting the CLI; keep permission/plugin and
// other user settings intact, including in a retained Sandbox after LLM-only restart.
export function refreshNativeClaudeSettings(env:NodeJS.ProcessEnv, home=env.HOME || homedir()):void {
  if(!env.AGENT_COMPOSE_RUN_ID || !env.MONKEYCODE_TASK_ID) return;
  const path=join(home,".claude","settings.json");
  let settings:any;
  try {
    const stat=lstatSync(path);
    if(!stat.isFile() || stat.size>1<<20 || realpathSync(path)!==resolve(path)) throw new Error("invalid settings file");
    settings=JSON.parse(readFileSync(path,"utf8"));
  } catch(error:any) {
    if(error?.code==="ENOENT") return; // No user settings to override the current environment.
    throw new Error("Native Claude settings unavailable");
  }
  if(!settings || typeof settings!=="object" || Array.isArray(settings)
      || (settings.env!==undefined && (!settings.env || typeof settings.env!=="object" || Array.isArray(settings.env)))) {
    throw new Error("Native Claude settings invalid");
  }
  const current={...settings.env};
  for(const key of ["ANTHROPIC_API_KEY","ANTHROPIC_AUTH_TOKEN","ANTHROPIC_BASE_URL","ANTHROPIC_MODEL"]) {
    if(typeof env[key]!=="string" || !env[key]?.trim()) throw new Error("Native Claude model environment incomplete");
    current[key]=env[key];
  }
  for(const key of ["ANTHROPIC_DEFAULT_HAIKU_MODEL","ANTHROPIC_DEFAULT_SONNET_MODEL","ANTHROPIC_DEFAULT_OPUS_MODEL"]) {
    if(key in current) current[key]=env.ANTHROPIC_MODEL;
  }
  const temporary=path+"."+randomUUID()+".tmp";
  try {
    writeFileSync(temporary,JSON.stringify({...settings,env:current}),{mode:0o600,flag:"wx"});
    renameSync(temporary,path);
  } catch { throw new Error("Native Claude settings update failed"); }
  finally { rmSync(temporary,{force:true}); }
}

export function sdkDecision(provider: string, tool: string, input: Record<string, unknown>, id: string, signal: AbortSignal, emit: (event: AgentEvent) => void): Promise<Record<string, unknown>> {
  const question = tool === "AskUserQuestion";
  const questions = question && Array.isArray(input.questions) ? input.questions.map((value: any) => ({question: value.question, multiple: !!value.multiSelect, custom: true, options: (value.options || []).map((option: any) => ({label:option.label}))})) : undefined;
  const request = {provider, native_id:id, kind:question ? "question" : "permission", questions, parent_pid:process.pid, title:tool+": "+JSON.stringify(input)};
  return new Promise((resolve,reject) => {
    const child = spawn("python3",["/opt/agent-compose-runtime/monkeycode-sdk-control.py",Buffer.from(JSON.stringify(request)).toString("base64")],{stdio:["ignore","pipe","ignore"]});
    let settled=false;
    const abort=()=>{child.kill("SIGTERM");if(!settled){settled=true;reject(new Error("native control canceled"));}};
    signal.addEventListener("abort",abort,{once:true});
    if(signal.aborted){abort();return;}
    createInterface({input:child.stdout}).on("line",line=>{
      try {
        const value=JSON.parse(line);
        if(value.event==="request" || value.event==="auto_reply") {
          emit({kind:"tool_call",id:value.metadata.request_id,name:value.event==="auto_reply"?"monkeycode_permission_reply":"question",toolKind:"other",status:value.event==="auto_reply"?"completed":"pending",input:value.metadata});
        } else if(value.event==="decision" && !settled) {settled=true;resolve(value.answer);}
      } catch { child.kill("SIGTERM");if(!settled){settled=true;reject(new Error("invalid native control response"));} }
    });
    child.on("error",()=>{if(!settled){settled=true;reject(new Error("native control unavailable"));}});
    child.on("exit",()=>{signal.removeEventListener("abort",abort);if(!settled){settled=true;reject(new Error("native control did not complete"));}});
  });
}

export async function claudeToolDecision(tool: string, input: Record<string, unknown>, options: {signal:AbortSignal;toolUseID:string}, emit:(event:AgentEvent)=>void):Promise<any> {
  try {
    const response=await sdkDecision("claude",tool,input,options.toolUseID,options.signal,emit);
    if(response.cancelled || (tool!=="AskUserQuestion" && JSON.stringify(response.answers)==='[["拒绝"]]')) return {behavior:"deny",message:"User declined this request",toolUseID:options.toolUseID};
    let updatedInput=input;
    if(tool==="AskUserQuestion") {
      const answers:Record<string,string>={};
      (input.questions as any[]).forEach((question,index)=>{answers[question.question]=(response.answers as string[][])[index].join(", ");});
      updatedInput={...input,answers};
    }
    return {behavior:"allow",updatedInput,toolUseID:options.toolUseID};
  } catch {return {behavior:"deny",message:"Native request canceled or unavailable",interrupt:true,toolUseID:options.toolUseID};}
}
