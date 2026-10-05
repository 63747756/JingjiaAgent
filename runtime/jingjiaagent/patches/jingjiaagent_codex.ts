// The exec SDK cannot answer native approvals. Use the pinned CLI's app-server
// protocol inside a Run, retaining its own thread store and native decisions.
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { resolveCodexPath } from "./codex-path.js";
import { readStoredThread, writeStoredThread } from "./session-state.js";
import { decideCodexThreadResume, hashSystemContext, codexThreadStateMetadata } from "./codex-thread-resume.js";
import { sdkDecision, nativeModelEnvironment } from "./jingjiaagent-sdk.js";
import { nativeAssets, nativeAttachments } from "./jingjiaagent-assets.js";
import type { AgentEvent } from "./agent-event.js";
import type { AgentResult, RunnerOptions } from "./types.js";

export async function nativeCodex(options:RunnerOptions, prompt:string, emit:(event:AgentEvent)=>void):Promise<AgentResult> {
  options=nativeAssets(options,"codex");
  const attached=nativeAttachments(prompt);
  const signal=options.abortController?.signal || new AbortController().signal;
  const child=spawn(resolveCodexPath(),["app-server","--listen","stdio://"],{cwd:options.workspace,env:nativeModelEnvironment("codex",process.env),stdio:["pipe","pipe","pipe"]});
  child.stderr.on("data",()=>{}); // Native diagnostics can reflect shared credentials.
  const result:AgentResult={provider:"codex",threadId:"",stopReason:"stop",finalText:"",finalTextSource:"provider_message",transcript:"",stderr:""};
  let sequence=0,settled=false;
  const pending=new Map<number,{resolve:(value:any)=>void;reject:(error:Error)=>void}>();
  const texts=new Map<string,string>();
  let usage:any;
  let complete:(value:AgentResult)=>void,fail:(error:Error)=>void;
  const finished=new Promise<AgentResult>((resolve,reject)=>{complete=resolve;fail=reject;});
  // Initialization can fail before the caller starts awaiting completion.
  void finished.catch(()=>{});
  const send=(value:unknown)=>child.stdin.write(JSON.stringify(value)+"\n");
  const rpc=(method:string,params:any)=>new Promise<any>((resolve,reject)=>{const id=++sequence;pending.set(id,{resolve,reject});send({jsonrpc:"2.0",id,method,params});});
  const fatal=()=>{if(!settled){settled=true;const error=new Error(signal.aborted?"native Codex canceled":"native Codex session failed");for(const waiter of pending.values())waiter.reject(error);pending.clear();fail(error);}};
  const abort=()=>{child.kill("SIGTERM");fatal();};
  signal.addEventListener("abort",abort,{once:true});
  child.on("error",fatal);child.on("exit",fatal);
  const itemEvent=(item:any,completed:boolean)=>{
    const id=String(item.id||"");
    if(item.type==="agentMessage") {
      const previous=texts.get(id)||"",next=String(item.text||"");
      if(completed && next.startsWith(previous) && next.length>previous.length)emit({kind:"text_delta",text:next.slice(previous.length)});
      if(completed){texts.set(id,next);result.finalText=next;}
    } else if(item.type==="commandExecution") {
      emit({kind:"tool_call",id,name:"shell",toolKind:"execute",status:completed?(item.exitCode===0?"completed":"failed"):"in_progress",command:String(item.command||""),...(completed?{exitCode:item.exitCode}: {})});
      if(completed)emit({kind:"tool_result",id,ok:item.exitCode===0,output:String(item.aggregatedOutput||"")});
    } else if(item.type==="fileChange") {
      emit({kind:"tool_call",id,name:"apply_patch",toolKind:"edit",status:completed?"completed":"in_progress",input:item.changes});
      if(completed)emit({kind:"tool_result",id,ok:item.status==="completed",output:JSON.stringify(item.changes||[])});
    } else if(item.type==="mcpToolCall") {
      emit({kind:"tool_call",id,name:String(item.server)+"/"+String(item.tool),toolKind:"other",status:completed?(item.error?"failed":"completed"):"in_progress",input:item.arguments});
      if(completed)emit({kind:"tool_result",id,ok:!item.error,output:JSON.stringify(item.result||item.error||{})});
    }
  };
  const answer=async(message:any)=>{
    const p=message.params||{};
    try {
      if(message.method==="item/tool/call" && p.tool==="AskUserQuestion") {
        const input=typeof p.arguments==="string"?JSON.parse(p.arguments):p.arguments;
        const decision=await sdkDecision("codex","AskUserQuestion",input,String(message.id),signal,emit);
        const answers:Record<string,string>={};
        (input.questions||[]).forEach((question:any,index:number)=>{answers[question.question]=decision.cancelled?"":(decision.answers as string[][])[index].join(", ");});
        send({jsonrpc:"2.0",id:message.id,result:{success:!decision.cancelled,contentItems:[{type:"inputText",text:JSON.stringify({answers,cancelled:!!decision.cancelled})}]}});
      } else if(message.method==="item/tool/requestUserInput") {
        const input={questions:(p.questions||[]).map((question:any)=>({...question,multiSelect:false}))};
        const decision=await sdkDecision("codex","AskUserQuestion",input,String(message.id),signal,emit);
        const answers:Record<string,unknown>={};
        (p.questions||[]).forEach((question:any,index:number)=>{answers[question.id]={answers:decision.cancelled?[]:(decision.answers as string[][])[index]};});
        send({jsonrpc:"2.0",id:message.id,result:{answers}});
      } else if(message.method==="item/commandExecution/requestApproval" || message.method==="item/fileChange/requestApproval") {
        const decision=await sdkDecision("codex",message.method,p,String(message.id),signal,emit);
        const accepted=!decision.cancelled && JSON.stringify(decision.answers)==='[["允许一次"]]';
        send({jsonrpc:"2.0",id:message.id,result:{decision:accepted?"accept":"decline"}});
      } else if(message.method==="mcpServer/elicitation/request" && p._meta?.codex_approval_kind==="mcp_tool_call") {
        // Native MCP tool approval is a distinct request in CLI 0.144.1.
        // An ordinary MCP form is never silently treated as a permission.
        const decision=await sdkDecision("codex","MCP: "+String(p.serverName),{message:p.message},String(message.id),signal,emit);
        const accepted=!decision.cancelled && JSON.stringify(decision.answers)==='[["允许一次"]]';
        send({jsonrpc:"2.0",id:message.id,result:{action:decision.cancelled?"cancel":accepted?"accept":"decline",content:accepted?{}:null}});
      } else if(message.method==="item/permissions/requestApproval") {
        const decision=await sdkDecision("codex",message.method,p,String(message.id),signal,emit);
        const accepted=!decision.cancelled && JSON.stringify(decision.answers)==='[["允许一次"]]';
        send({jsonrpc:"2.0",id:message.id,result:{permissions:accepted?p.permissions:{},scope:"turn"}});
      } else {
        emit({kind:"error",severity:"warning",message:"Native Codex control capability unavailable: "+String(message.method)});
        send({jsonrpc:"2.0",id:message.id,error:{code:-32601,message:"Native control capability unavailable"}});
      }
    } catch {send({jsonrpc:"2.0",id:message.id,error:{code:-32000,message:"Native control canceled or unavailable"}});}
  };
  createInterface({input:child.stdout}).on("line",line=>{
    try {
      const message=JSON.parse(line);
      if(message.id!==undefined && message.method){void answer(message);return;}
      if(message.id!==undefined){const waiter=pending.get(message.id);if(waiter){pending.delete(message.id);if(message.error)waiter.reject(new Error("Native Codex RPC rejected"));else waiter.resolve(message.result);}return;}
      const p=message.params||{};
      switch(message.method){
        case "item/started":itemEvent(p.item,false);break;
        case "item/completed":itemEvent(p.item,true);break;
        case "item/agentMessage/delta": {
          const delta=String(p.delta||"");texts.set(p.itemId,(texts.get(p.itemId)||"")+delta);emit({kind:"text_delta",text:delta});break;
        }
        case "item/reasoning/summaryTextDelta":case "item/reasoning/textDelta":emit({kind:"reasoning_delta",text:String(p.delta||"")});break;
        case "thread/tokenUsage/updated":usage=p.tokenUsage?.last;break;
        case "turn/completed":
          if(p.turn?.status!=="completed"){fatal();break;}
          if(usage)emit({kind:"usage",scope:"turn",inputTokens:Math.max((usage.inputTokens||0)-(usage.cachedInputTokens||0),0),outputTokens:usage.outputTokens||0,cachedTokens:usage.cachedInputTokens||0,reasoningTokens:usage.reasoningOutputTokens||0});
          result.transcript=result.finalText;settled=true;complete(result);break;
      }
    } catch {fatal();}
  });
  try {
    if(signal.aborted)throw new Error("native Codex canceled");
    await rpc("initialize",{clientInfo:{name:"jingjiaagent",title:"JingjiaAgent runtime",version:"1"},capabilities:{experimentalApi:true}});
    send({jsonrpc:"2.0",method:"initialized"});
    const contextHash=hashSystemContext(options.systemContext);
    const stored=await readStoredThread(options.sessionRoot,"codex");
    const resume=decideCodexThreadResume(stored,contextHash);
    const parameters={cwd:options.workspace,model:options.model,sandbox:"danger-full-access",approvalPolicy:"untrusted",...(options.systemContext?{developerInstructions:options.systemContext}:{}),config:{...(options.effort?{model_reasoning_effort:options.effort}:{})}};
    const questionTool={
      type:"function",name:"AskUserQuestion",
      description:"Ask the user a question and wait for their explicit answer. Never choose the answer yourself.",
      inputSchema:{
        type:"object",required:["questions"],properties:{
          questions:{type:"array",minItems:1,maxItems:16,items:{
            type:"object",required:["question","options"],properties:{
              question:{type:"string"},header:{type:"string"},multiSelect:{type:"boolean"},
              options:{type:"array",items:{
                type:"object",required:["label"],properties:{label:{type:"string"},description:{type:"string"}}
              }}
            }
          }}
        }
      }
    };
    const created=await rpc(resume.action==="resume"?"thread/resume":"thread/start",resume.action==="resume"?{...parameters,threadId:resume.threadId}:{...parameters,dynamicTools:[questionTool]});
    result.threadId=created.thread.id;
    await writeStoredThread(options.sessionRoot,"codex",result.threadId,new Date(),codexThreadStateMetadata(contextHash));
    await rpc("turn/start",{threadId:result.threadId,input:[{type:"text",text:attached.text},...attached.files.filter(item=>item.mime.startsWith("image/")).map(item=>({type:"localImage",path:item.path}))],...(options.outputSchema?{outputSchema:options.outputSchema}:{})});
    return await finished;
  } finally {
    signal.removeEventListener("abort",abort);
    for(const waiter of pending.values())waiter.reject(new Error("Native Codex session closed"));pending.clear();
    child.kill("SIGTERM");
  }
}
