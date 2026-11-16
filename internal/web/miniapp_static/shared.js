// Общая часть двух дизайнов кабинета и мини-аппа (index.html и minimal.html).
// Сервер вставляет этот файл в страницу на место своей метки — отдельного
// запроса за ним нет. Здесь только функции, одинаковые в обоих дизайнах:
// правка делается один раз. Глобальные WEB, STATE, api и прочее объявляет
// основной скрипт страницы, функции обращаются к ним в момент вызова.

// Сторонний чат поддержки: только кабинет в браузере, скрипт ставится один раз.
var CHAT_ON=false;
function mountChat(src){
  if(CHAT_ON||!WEB||!src||!/^https:\/\//i.test(src))return;
  CHAT_ON=true;
  var s=document.createElement('script');s.src=src;s.async=true;document.body.appendChild(s);
}
function guestChat(){api('/api/cabinet/config').then(function(c){mountChat(c.chat_src);},function(){});}
// Параметры ссылки, по которой открыли кабинет: ?promo=, ?plan=, ?ref=, ?inv=
// или ?start=. Работают как /start в чате, их можно сочетать. Хранятся одной
// строкой через перевод строки.
function startParams(){
  var out=[];
  try{var q=new URLSearchParams(location.search);
    ['ref','inv','promo','plan'].forEach(function(k){var v=q.get(k);if(v)out.push(k+'_'+v);});
    var s=q.get('start');if(s)out.push(s);
  }catch(e){}
  // Разделитель — перевод строки: в коде промокода бывает что угодно, кроме
  // пробельных символов.
  return out.map(function(t){return t.replace(/\s/g,'').slice(0,64);}).filter(Boolean).join('\n');
}
// На сервер при входе уходит одно: приглашение в закрытый бот или
// пригласивший друг.
function serverStart(s){
  var t=String(s||'').split('\n');
  return t.filter(function(x){return x.indexOf('inv_')===0;})[0]||t.filter(function(x){return x.indexOf('ref_')===0;})[0]||'';
}
function setDesk(){DESK=!!(DESKMQ&&DESKMQ.matches);try{document.body.classList.toggle('desk',DESK);}catch(e){}}
function avaHTML(ini){return AVA&&AVA_ID&&AVA_ID===avaID()?'<img src="'+AVA+'" alt="">':esc(ini);}
function loadAva(){
  var id=avaID();
  if(!TOKEN||id<=0||AVA_ID===id)return;
  AVA_ID=id;if(AVA){try{URL.revokeObjectURL(AVA);}catch(e){}AVA='';}
  fetch('/api/miniapp/avatar',{headers:{'Authorization':'Bearer '+TOKEN}}).then(function(r){return r.ok?r.blob():null;}).then(function(b){
    if(!b||AVA_ID!==id||!window.URL)return;
    AVA=URL.createObjectURL(b);
    Array.prototype.forEach.call(document.querySelectorAll('[data-ava]'),function(e){e.innerHTML='<img src="'+AVA+'" alt="">';});
  }).catch(function(){});
}
function isTgLink(u){return /^(https:\/\/t\.me\/|tg:\/\/)/i.test(String(u||''));}
function planTitle(p){return (p.icon?p.icon+' ':'')+(p.name||p.code);}
function daysLeft(ts){var d=Math.ceil((ts*1000-Date.now())/86400000);return d>0?d:0;}
function payTabDrop(w){try{if(w&&!w.closed)w.close();}catch(e){}}
function legalDocs(){return (STATE.menu&&STATE.menu.legal)||[];}
// tgNonce — пара verifier/nonce: nonce уходит в Telegram и попадает в токен,
// verifier — только на наш сервер. Без verifier перехваченный токен не годится.
function tgNonce(){
  try{
    if(!(window.crypto&&crypto.subtle&&crypto.getRandomValues&&window.TextEncoder))return Promise.resolve({v:'',n:''});
    var b=new Uint8Array(32);crypto.getRandomValues(b);
    var v=btoa(String.fromCharCode.apply(null,b)).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'');
    return crypto.subtle.digest('SHA-256',new TextEncoder().encode(v)).then(function(h){
      return {v:v,n:Array.prototype.map.call(new Uint8Array(h),function(x){return ('0'+x.toString(16)).slice(-2);}).join('')};
    },function(){return {v:'',n:''};});
  }catch(e){return Promise.resolve({v:'',n:''});}
}

// Значение внутри onclick="…('значение')": экранируются и HTML, и строка JS,
// включая переводы строк — иначе обработчик кнопки ломается.
function jsq(s){return String(s==null?'':s).replace(/\\/g,'\\\\').replace(/'/g,"\\'").replace(/&/g,'&amp;').replace(/"/g,'&quot;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/\n/g,'\\n').replace(/\r/g,'\\r');}
// Копирование без Clipboard API (старые WebView).
function fallbackCopy(t){try{var ta=document.createElement('textarea');ta.value=t;ta.setAttribute('readonly','');ta.style.position='fixed';ta.style.left='-9999px';document.body.appendChild(ta);ta.select();ta.setSelectionRange(0,String(t).length);var ok=document.execCommand('copy');document.body.removeChild(ta);return ok;}catch(e){return false;}}
async function post(path,body){return api(path,{method:'POST',body:JSON.stringify(body||{})});}
function mkErr(st,m){var e=new Error(m||('HTTP '+st));e.status=st;return e;}
// true — ссылку открыл сам Telegram.
function openSmart(u){if(isTgLink(u)&&tg&&tg.openTelegramLink){try{tg.openTelegramLink(u);return true;}catch(e){}}openL(u);return false;}
