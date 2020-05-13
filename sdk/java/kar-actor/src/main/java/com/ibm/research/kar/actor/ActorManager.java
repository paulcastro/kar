package com.ibm.research.kar.actor;

import java.lang.reflect.Method;

public interface ActorManager {
	// create actor instance
	public Object createActor(String type, String id);
	
	// delete actor instance
	public void deleteActor(String type, String id);
	
	// get existing or create new actor instance
	public Object getActor(String type, String id);
	
	public Method getActorMethod(String type, String name);
	
}