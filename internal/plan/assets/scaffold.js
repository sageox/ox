// ox plan — portable scaffold runtime. No build step, runs from file://.
// Mermaid is the only external dep (CDN, at view time). Handles dark/light
// re-render, OS-preference default + persistence, tabbed section views
// (plans with >3 sections), and scroll-spy TOC (single-scroll mode).
(function(){
  var darkVars={background:'#111411',primaryColor:'#171b17',primaryTextColor:'#e8ede7',primaryBorderColor:'#252c24',lineColor:'#6f7a6d',secondaryColor:'#141814',tertiaryColor:'#121612',fontFamily:'Inter, sans-serif',fontSize:'13px',actorBkg:'#171b17',actorBorder:'#252c24',actorTextColor:'#e8ede7',noteBkgColor:'#241803',noteTextColor:'#f7d8a0',noteBorderColor:'#f59e0b'};
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
  var links=[].slice.call(document.querySelectorAll('nav.toc a'));
  // tabbed views (>3 sections): the server renders a .tabbar; JS switches the
  // visible section. Hiding is gated on body.tabs-on so a no-JS open still
  // shows every section as one scroll (graceful degradation).
  var tabbar=document.querySelector('.tabbar');
  if(tabbar){
    document.body.classList.add('tabs-on');
    var views=[].slice.call(document.querySelectorAll('main > section[id^="sec-"]'));
    var tabs=[].slice.call(tabbar.querySelectorAll('button[data-tab]'));
    var activate=function(id,remember){
      views.forEach(function(v){v.classList.toggle('on',v.id===id);});
      tabs.forEach(function(b){b.setAttribute('aria-selected',String(b.getAttribute('data-tab')===id));});
      links.forEach(function(l){l.classList.toggle('active',l.getAttribute('href')==='#'+id);});
      if(remember&&history.replaceState)history.replaceState(null,'','#'+id);
      // a diagram laid out inside a hidden tab has zero width — re-render on reveal
      renderMer();
    };
    tabs.forEach(function(b){b.addEventListener('click',function(){activate(b.getAttribute('data-tab'),true);});});
    // TOC entries drive tabs too: one nav model, two affordances.
    links.forEach(function(l){var id=l.getAttribute('href').slice(1);if(id.indexOf('sec-')!==0)return;l.addEventListener('click',function(e){e.preventDefault();activate(id,true);});});
    var initial=(location.hash||'').slice(1);
    if(!views.some(function(v){return v.id===initial;}))initial=views.length?views[0].id:'';
    if(initial)activate(initial,false);
  }
  // scroll-spy over section headings (single-scroll mode; in tabbed mode the
  // active TOC entry mirrors the active tab instead)
  if(!tabbar&&window.IntersectionObserver){
    var map={};links.forEach(function(a){map[a.getAttribute('href').slice(1)]=a;});
    var obs=new IntersectionObserver(function(es){es.forEach(function(e){if(e.isIntersecting){links.forEach(function(l){l.classList.remove('active');});var a=map[e.target.id];if(a)a.classList.add('active');}});},{rootMargin:'-12% 0px -75% 0px'});
    document.querySelectorAll('section[id]').forEach(function(s){obs.observe(s);});
  }
  function start(){renderMer();}
  if(document.readyState!=='loading')start();else document.addEventListener('DOMContentLoaded',start);
})();
