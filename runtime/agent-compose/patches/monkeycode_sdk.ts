import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import type { AgentEvent } from "./agent-event.js";

// Platform CLI settings point at the product model proxy. The daemon's facade
// replaces conventional API-key variables; restore a matching pair for native
// CLIs instead of mixing credentials and endpoints from the two routing layers.
export function nativeModelEnvironment(provider:string, source:NodeJS.ProcessEnv):NodeJS.ProcessEnv {
  const env={...source};
  if(!env.AGENT_COMPOSE_RUN_ID || !env.MONKEYCODE_TASK_ID || !env.MONKEYCODE_MODEL_API_KEY) return env;
  const key=env.MONKEYCODE_MODEL_API_KEY,base=env.MONKEYCODE_MODEL_BASE_URL,model=env.MONKEYCODE_MODEL_NAME;
  if(!base || !model)throw new Error("Native platform model configuration is incomplete");
  if(provider==="claude") {
    env.ANTHROPIC_API_KEY=key;env.ANTHROPIC_AUTH_TOKEN=key;env.ANTHROPIC_BASE_URL=base;env.ANTHROPIC_MODEL=model;
  } else {
    env.OPENAI_API_KEY=key;env.OPENAI_BASE_URL=base;env.OPENAI_MODEL=model;
  }
  return env;
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
