// Run inside an ephemeral pinned Guest; this checks filesystem ownership and
// current selections independently of model compliance.
import assert from 'node:assert/strict';
import { randomUUID, createHash } from 'node:crypto';
import { mkdtempSync, mkdirSync, writeFileSync, existsSync, lstatSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { nativeAssets, nativeAttachments, claudePrompt } from '/opt/agent-compose-runtime/dist/jingjiaagent-assets.js';

const home=mkdtempSync('/tmp/jingjiaagent-assets-'), task=randomUUID();
process.env.JINGJIAAGENT_TASK_ID=task;
const root=join(home,'.codingmatrix/project-tpl/.ai-ready');
const state='/data/state/jingjiaagent-native';
const attachmentState='/data/state/jingjiaagent-attachments';
const put=(path,value)=>{mkdirSync(join(path,'..'),{recursive:true});writeFileSync(path,value);};
const save=(path,value)=>put(path,JSON.stringify(value));
const options={home,systemContext:'BASE'};
try {
  put(join(root,'rules/selected.md'),'SELECTED');put(join(root,'rules/stale.md'),'STALE');
  save(join(state,task+'.rules.json'),{task_id:task,rules:[join(root,'rules/selected.md')]});
  put(join(root,'skills/proof/SKILL.md'),'---\nname: proof\ndescription: test\n---\nSKILL');
  save(join(root,'.runtime-resources.json'),{resources:{skills:[{name:'proof'}]}});
  for(const provider of ['claude','codex']) {
    const target=join(home,provider==='claude'?'.claude/skills':'.agents/skills');
    put(join(target,'user-existing/SKILL.md'),'EMPLOYEE');
    const installed=nativeAssets(options,provider);
    assert(installed.systemContext.includes('SELECTED'));assert(!installed.systemContext.includes('STALE'));
    assert(lstatSync(join(target,'proof')).isSymbolicLink());
    save(join(root,'.runtime-resources.json'),{resources:{skills:[]}});
    assert.equal(nativeAssets(options,provider).skills.length,0);
    assert(!existsSync(join(target,'proof')));assert(existsSync(join(target,'user-existing/SKILL.md')));
    save(join(root,'.runtime-resources.json'),{resources:{skills:[{name:'proof'}]}});
    mkdirSync(join(target,'proof'),{recursive:true});
    assert.throws(()=>nativeAssets(options,provider),/already owned/);
    assert(lstatSync(join(target,'proof')).isDirectory());
  }
  const command=randomUUID(),path=join('/workspace/.jingjiaagent/attachments',task,command,'1-中文.txt');
  const bytes=Buffer.from('真实中文内容');put(path,bytes);
  const pointer=join(attachmentState,task+'.json');
  save(pointer,{task_id:task,command_id:command,files:[{path,filename:'中文.txt',mime:'text/plain',size:bytes.length,sha256:createHash('sha256').update(bytes).digest('hex')}]});
  assert(nativeAttachments('PROMPT').text.includes(path));
  put(path,Buffer.alloc(bytes.length,0));assert.throws(()=>nativeAttachments('PROMPT'),/content changed/);
  save(pointer,{task_id:task,command_id:command,files:[]});assert.equal(nativeAttachments('PROMPT').text,'PROMPT');
  assert.equal(claudePrompt('PROMPT'),'PROMPT');
  console.log('PASS native assets: only selected rules, owned skills cleared, employee paths preserved, attachment integrity and empty selection enforced');
} finally {
  rmSync(home,{recursive:true,force:true});
  for(const path of [join(state,task+'.rules.json'),join(state,'claude-'+task+'.skills.json'),join(state,'codex-'+task+'.skills.json'),join(attachmentState,task+'.json')])rmSync(path,{force:true});
  rmSync(join('/workspace/.jingjiaagent/attachments',task),{recursive:true,force:true});
}
