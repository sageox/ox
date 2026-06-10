// ox plan — portable scaffold runtime. No build step, runs from file://.
// Mermaid is the only external dep (CDN, at view time). Handles dark/light
// re-render, OS-preference default + persistence, and scroll-spy TOC.
(function(){
  var darkVars={background:'#151b1e',primaryColor:'#1a2226',primaryTextColor:'#e6edf0',primaryBorderColor:'#2a3439',lineColor:'#5f7178',secondaryColor:'#16201f',tertiaryColor:'#141d20',fontFamily:'Inter, sans-serif',fontSize:'13px',actorBkg:'#1a2226',actorBorder:'#2a3439',actorTextColor:'#e6edf0',noteBkgColor:'#241803',noteTextColor:'#f7d8a0',noteBorderColor:'#f59e0b'};
  var lightVars={background:'#ffffff',primaryColor:'#f5f8f4',primaryTextColor:'#16201c',primaryBorderColor:'#d8e0d8',lineColor:'#6c7a72',secondaryColor:'#eef4ee',tertiaryColor:'#eef7f5',fontFamily:'Inter, sans-serif',fontSize:'13px',actorBkg:'#f5f8f4',actorBorder:'#d8e0d8',actorTextColor:'#16201c',noteBkgColor:'#fdf2dc',noteTextColor:'#6b4a0c',noteBorderColor:'#b4730c'};
  var nodes=[].slice.call(document.querySelectorAll('.mermaid'));
  var srcs=nodes.map(function(n){return n.textContent;});
  function renderMer(){
    if(typeof mermaid==='undefined')return;
    var dark=document.documentElement.getAttribute('data-theme')!=='light';
    nodes.forEach(function(n,i){n.removeAttribute('data-processed');n.textContent=srcs[i];});
    mermaid.initialize({startOnLoad:false,theme:'base',themeVariables:dark?darkVars:lightVars,flowchart:{nodeSpacing:34,rankSpacing:34,padding:8,useMaxWidth:true},sequence:{useMaxWidth:true},state:{useMaxWidth:true},securityLevel:'antiscript'});
    try{mermaid.run({nodes:document.querySelectorAll('.mermaid')});}catch(e){}
  }
  var root=document.documentElement,btn=document.getElementById('themeBtn');
  var saved=null;try{saved=localStorage.getItem('ox-plan-theme');}catch(e){}
  if(saved)root.setAttribute('data-theme',saved);
  else if(window.matchMedia&&window.matchMedia('(prefers-color-scheme: light)').matches)root.setAttribute('data-theme','light');
  if(btn)btn.onclick=function(){var next=root.getAttribute('data-theme')==='light'?'dark':'light';root.setAttribute('data-theme',next);try{localStorage.setItem('ox-plan-theme',next);}catch(e){}renderMer();};
  // scroll-spy over section headings
  var links=[].slice.call(document.querySelectorAll('nav.toc a'));
  var map={};links.forEach(function(a){map[a.getAttribute('href').slice(1)]=a;});
  if(window.IntersectionObserver){
    var obs=new IntersectionObserver(function(es){es.forEach(function(e){if(e.isIntersecting){links.forEach(function(l){l.classList.remove('active');});var a=map[e.target.id];if(a)a.classList.add('active');}});},{rootMargin:'-12% 0px -75% 0px'});
    document.querySelectorAll('section[id]').forEach(function(s){obs.observe(s);});
  }
  function start(){renderMer();}
  if(document.readyState!=='loading')start();else document.addEventListener('DOMContentLoaded',start);
})();
